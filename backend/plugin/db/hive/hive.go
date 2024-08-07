package hive

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/beltran/gohive"
	"github.com/pkg/errors"
	"go.uber.org/multierr"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/bytebase/bytebase/backend/common"
	"github.com/bytebase/bytebase/backend/plugin/db"
	"github.com/bytebase/bytebase/backend/plugin/db/util"
	"github.com/bytebase/bytebase/backend/plugin/parser/base"

	// register splitter functions init().
	_ "github.com/bytebase/bytebase/backend/plugin/parser/standard"
	storepb "github.com/bytebase/bytebase/proto/generated-go/store"
	v1pb "github.com/bytebase/bytebase/proto/generated-go/v1"
)

func hiveDriverFunc(db.DriverConfig) db.Driver {
	return &Driver{}
}

func init() {
	db.Register(storepb.Engine_HIVE, hiveDriverFunc)
}

type Driver struct {
	config   db.ConnectionConfig
	ctx      db.ConnectionContext
	connPool *FixedConnPool
	conn     *gohive.Connection
}

var (
	_          db.Driver = (*Driver)(nil)
	numMaxConn           = 5
)

func (d *Driver) Open(_ context.Context, _ storepb.Engine, config db.ConnectionConfig) (db.Driver, error) {
	if config.Host == "" {
		return nil, errors.Errorf("hostname not set")
	}

	d.config = config
	d.ctx = config.ConnectionContext

	if d.connPool == nil {
		if config.SASLConfig == nil {
			config.SASLConfig = &db.PlainSASLConfig{
				Username: config.Username,
				Password: config.Password,
			}
		}
		if !config.SASLConfig.Check() {
			return nil, errors.New("SASL settings error")
		}
		pool, err := CreateHiveConnPool(numMaxConn, &config)
		if err != nil {
			return nil, err
		}
		d.connPool = pool
	}

	newConn, err := d.connPool.Get(config.Database)
	if err != nil {
		err = errors.Wrapf(err, "failed to get connection from pool")
		// release resources.
		if closeErr := d.connPool.Destroy(); closeErr != nil {
			err = multierr.Combine(closeErr, err)
		}
		return nil, err
	}

	d.conn = newConn

	return d, nil
}

func (d *Driver) Close(_ context.Context) error {
	d.connPool.Put(d.conn)
	return d.connPool.Destroy()
}

func (d *Driver) Ping(ctx context.Context) error {
	if d.conn == nil {
		return errors.Errorf("no database connection established")
	}
	cursor := d.conn.Cursor()
	defer cursor.Close()

	cursor.Exec(ctx, "SELECT 1")
	if cursor.Err != nil {
		return errors.Errorf("bad connection")
	}
	return nil
}

func (*Driver) GetDB() *sql.DB {
	return nil
}

// Transaction statements [BEGIN, COMMIT, ROLLBACK] are not supported in Hive 4.0 temporarily.
// Even in Hive's bucketed transaction table, all the statements are committed automatically by
// the Hive server.
func (d *Driver) Execute(ctx context.Context, statementsStr string, _ db.ExecuteOptions) (int64, error) {
	if d.connPool == nil {
		return 0, errors.Errorf("no database connection established")
	}

	var affectedRows int64

	cursor := d.conn.Cursor()
	defer cursor.Close()

	statements, err := base.SplitMultiSQL(storepb.Engine_HIVE, statementsStr)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to split statements")
	}

	for _, statement := range statements {
		cursor.Execute(ctx, strings.TrimRight(statement.Text, ";"), false)
		if cursor.Err != nil {
			return 0, errors.Wrap(cursor.Err, "failed to execute statement")
		}
		operationStatus := cursor.Poll(false)
		affectedRows += operationStatus.GetNumModifiedRows()
	}

	return affectedRows, nil
}

func (d *Driver) QueryConn(ctx context.Context, _ *sql.Conn, statement string, queryCtx *db.QueryContext) ([]*v1pb.QueryResult, error) {
	if d.connPool == nil {
		return nil, errors.Errorf("no database connection established")
	}

	singleSQLs, err := base.SplitMultiSQL(storepb.Engine_HIVE, statement)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to split statements")
	}

	conn, err := d.connPool.Get("")
	if err != nil {
		return nil, err
	}
	defer d.connPool.Put(conn)

	var results []*v1pb.QueryResult
	for _, singleSQL := range singleSQLs {
		statement := util.TrimStatement(singleSQL.Text)
		if queryCtx != nil && queryCtx.Explain {
			statement = fmt.Sprintf("EXPLAIN %s", statement)
		}

		result, err := runSingleStatement(ctx, conn, statement)
		if err != nil && result == nil {
			return results, err
		}

		results = append(results, result)
	}
	return results, nil
}

// This function converts basic types to types that have implemented isRowValue_Kind interface.
func parseValueType(value any, gohiveType string) (*v1pb.RowValue, error) {
	var rowValue v1pb.RowValue
	if value == nil {
		rowValue.Kind = &v1pb.RowValue_StringValue{StringValue: ""}
	} else {
		switch gohiveType {
		case "BOOLEAN_TYPE":
			rowValue.Kind = &v1pb.RowValue_BoolValue{BoolValue: value.(bool)}
		case "TINYINT_TYPE":
			rowValue.Kind = &v1pb.RowValue_Int32Value{Int32Value: int32(value.(int8))}
		case "SMALLINT_TYPE":
			rowValue.Kind = &v1pb.RowValue_Int32Value{Int32Value: int32(value.(int16))}
		case "INT_TYPE":
			rowValue.Kind = &v1pb.RowValue_Int32Value{Int32Value: value.(int32)}
		case "BIGINT_TYPE":
			rowValue.Kind = &v1pb.RowValue_Int64Value{Int64Value: value.(int64)}
		// dangerous truncation: float64 -> float32.
		case "FLOAT_TYPE":
			rowValue.Kind = &v1pb.RowValue_FloatValue{FloatValue: float32(value.(float64))}
		case "BINARY_TYPE":
			rowValue.Kind = &v1pb.RowValue_BytesValue{BytesValue: value.([]byte)}
		case "DOUBLE_TYPE":
			// convert float64 to string to avoid trancation.
			rowValue.Kind = &v1pb.RowValue_StringValue{StringValue: strconv.FormatFloat(value.(float64), 'f', 20, 64)}
		default:
			// convert all remaining types to string.
			rowValue.Kind = &v1pb.RowValue_StringValue{StringValue: value.(string)}
		}
	}
	return &rowValue, nil
}

func runSingleStatement(ctx context.Context, conn *gohive.Connection, statement string) (*v1pb.QueryResult, error) {
	startTime := time.Now()

	cursor := conn.Cursor()
	defer cursor.Close()

	// run query.
	cursor.Execute(ctx, statement, false)
	if cursor.Err != nil {
		return nil, errors.Wrap(cursor.Err, "failed to execute statement")
	}

	result := &v1pb.QueryResult{
		Statement: statement,
	}

	// We will get an error when a certain statement doesn't need returned results.
	columnNamesAndTypes := cursor.Description()
	for _, row := range columnNamesAndTypes {
		result.ColumnNames = append(result.ColumnNames, row[0])
	}

	// process query results.
	for cursor.HasMore(ctx) {
		var queryRow v1pb.QueryRow
		rowMap := cursor.RowMap(ctx)
		for idx, columnName := range result.ColumnNames {
			gohiveTypeStr := columnNamesAndTypes[idx][1]
			val, err := parseValueType(rowMap[columnName], gohiveTypeStr)
			if err != nil {
				return result, err
			}
			queryRow.Values = append(queryRow.Values, val)
		}

		// Rows.
		result.Rows = append(result.Rows, &queryRow)
		n := len(result.Rows)
		if (n&(n-1) == 0) && proto.Size(result) > common.MaximumSQLResultSize {
			result.Error = common.MaximumSQLResultSizeExceeded
			break
		}
	}
	result.Latency = durationpb.New(time.Since(startTime))
	return result, nil
}
