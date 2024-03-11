package mysql

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/pkg/errors"

	storepb "github.com/bytebase/bytebase/proto/generated-go/store"
)

type databaseState struct {
	name    string
	schemas map[string]*schemaState
}

func newDatabaseState() *databaseState {
	return &databaseState{
		schemas: make(map[string]*schemaState),
	}
}

func convertToDatabaseState(database *storepb.DatabaseSchemaMetadata) *databaseState {
	state := newDatabaseState()
	state.name = database.Name
	for _, schema := range database.Schemas {
		state.schemas[schema.Name] = convertToSchemaState(schema)
	}
	return state
}

func (s *databaseState) convertToDatabaseMetadata() *storepb.DatabaseSchemaMetadata {
	schemaStates := []*schemaState{}
	for _, schema := range s.schemas {
		schemaStates = append(schemaStates, schema)
	}
	sort.Slice(schemaStates, func(i, j int) bool {
		return schemaStates[i].id < schemaStates[j].id
	})
	schemas := []*storepb.SchemaMetadata{}
	for _, schema := range schemaStates {
		schemas = append(schemas, schema.convertToSchemaMetadata())
	}
	return &storepb.DatabaseSchemaMetadata{
		Name:    s.name,
		Schemas: schemas,
		// Unsupported, for tests only.
		Extensions: []*storepb.ExtensionMetadata{},
	}
}

type schemaState struct {
	id     int
	name   string
	tables map[string]*tableState
}

func newSchemaState() *schemaState {
	return &schemaState{
		tables: make(map[string]*tableState),
	}
}

func convertToSchemaState(schema *storepb.SchemaMetadata) *schemaState {
	state := newSchemaState()
	state.name = schema.Name
	for i, table := range schema.Tables {
		state.tables[table.Name] = convertToTableState(i, table)
	}
	return state
}

func (s *schemaState) convertToSchemaMetadata() *storepb.SchemaMetadata {
	tableStates := []*tableState{}
	for _, table := range s.tables {
		tableStates = append(tableStates, table)
	}
	sort.Slice(tableStates, func(i, j int) bool {
		return tableStates[i].id < tableStates[j].id
	})
	tables := []*storepb.TableMetadata{}
	for _, table := range tableStates {
		tables = append(tables, table.convertToTableMetadata())
	}
	return &storepb.SchemaMetadata{
		Name:   s.name,
		Tables: tables,
		// Unsupported, for tests only.
		Views:             []*storepb.ViewMetadata{},
		Functions:         []*storepb.FunctionMetadata{},
		Streams:           []*storepb.StreamMetadata{},
		Tasks:             []*storepb.TaskMetadata{},
		MaterializedViews: []*storepb.MaterializedViewMetadata{},
	}
}

type tableState struct {
	id          int
	name        string
	columns     map[string]*columnState
	indexes     map[string]*indexState
	foreignKeys map[string]*foreignKeyState
	comment     string
	// engine and collation is only supported in ParseToMetadata.
	engine                string
	collation             string
	partitionStateWrapper *partitionStateWrapper
}

func (t *tableState) toString(buf io.StringWriter) error {
	if _, err := buf.WriteString(fmt.Sprintf("CREATE TABLE `%s` (\n  ", t.name)); err != nil {
		return err
	}
	columns := []*columnState{}
	for _, column := range t.columns {
		columns = append(columns, column)
	}
	sort.Slice(columns, func(i, j int) bool {
		return columns[i].id < columns[j].id
	})
	for i, column := range columns {
		if i > 0 {
			if _, err := buf.WriteString(",\n  "); err != nil {
				return err
			}
		}
		if err := column.toString(buf); err != nil {
			return err
		}
	}

	indexes := []*indexState{}
	for _, index := range t.indexes {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i].id < indexes[j].id
	})

	for i, index := range indexes {
		if i+len(columns) > 0 {
			if _, err := buf.WriteString(",\n  "); err != nil {
				return err
			}
		}
		if err := index.toString(buf); err != nil {
			return err
		}
	}

	foreignKeys := []*foreignKeyState{}
	for _, fk := range t.foreignKeys {
		foreignKeys = append(foreignKeys, fk)
	}
	sort.Slice(foreignKeys, func(i, j int) bool {
		return foreignKeys[i].id < foreignKeys[j].id
	})

	for i, fk := range foreignKeys {
		if i+len(columns)+len(indexes) > 0 {
			if _, err := buf.WriteString(",\n  "); err != nil {
				return err
			}
		}
		if err := fk.toString(buf); err != nil {
			return err
		}
	}

	if _, err := buf.WriteString("\n)"); err != nil {
		return err
	}

	if t.engine != "" {
		if _, err := buf.WriteString(fmt.Sprintf(" ENGINE=%s", t.engine)); err != nil {
			return err
		}
	}

	if t.collation != "" {
		if _, err := buf.WriteString(fmt.Sprintf(" COLLATE=%s", t.collation)); err != nil {
			return err
		}
	}

	if t.comment != "" {
		if _, err := buf.WriteString(fmt.Sprintf(" COMMENT '%s'", strings.ReplaceAll(t.comment, "'", "''"))); err != nil {
			return err
		}
	}

	if t.partitionStateWrapper != nil {
		if _, err := buf.WriteString("\n"); err != nil {
			return err
		}
		if err := t.partitionStateWrapper.toString(buf); err != nil {
			return err
		}
	}

	if _, err := buf.WriteString(";\n"); err != nil {
		return err
	}
	return nil
}

func newTableState(id int, name string) *tableState {
	return &tableState{
		id:          id,
		name:        name,
		columns:     make(map[string]*columnState),
		indexes:     make(map[string]*indexState),
		foreignKeys: make(map[string]*foreignKeyState),
	}
}

func convertToTableState(id int, table *storepb.TableMetadata) *tableState {
	state := newTableState(id, table.Name)
	state.comment = table.Comment
	state.engine = table.Engine
	state.collation = table.Collation
	for i, column := range table.Columns {
		state.columns[column.Name] = convertToColumnState(i, column)
	}
	for i, index := range table.Indexes {
		state.indexes[index.Name] = convertToIndexState(i, index)
	}
	for i, fk := range table.ForeignKeys {
		state.foreignKeys[fk.Name] = convertToForeignKeyState(i, fk)
	}
	state.partitionStateWrapper = convertToPartitionStateWrapper(table.Partitions)
	return state
}

func (t *tableState) convertToTableMetadata() *storepb.TableMetadata {
	columnStates := []*columnState{}
	for _, column := range t.columns {
		columnStates = append(columnStates, column)
	}
	sort.Slice(columnStates, func(i, j int) bool {
		return columnStates[i].id < columnStates[j].id
	})
	columns := []*storepb.ColumnMetadata{}
	for _, column := range columnStates {
		columns = append(columns, column.convertToColumnMetadata())
	}
	// Backfill all the column positions.
	for i, column := range columns {
		column.Position = int32(i + 1)
	}

	indexStates := []*indexState{}
	for _, index := range t.indexes {
		indexStates = append(indexStates, index)
	}
	sort.Slice(indexStates, func(i, j int) bool {
		return indexStates[i].id < indexStates[j].id
	})
	indexes := []*storepb.IndexMetadata{}
	for _, index := range indexStates {
		indexes = append(indexes, index.convertToIndexMetadata())
	}

	fkStates := []*foreignKeyState{}
	for _, fk := range t.foreignKeys {
		fkStates = append(fkStates, fk)
	}
	sort.Slice(fkStates, func(i, j int) bool {
		return fkStates[i].id < fkStates[j].id
	})
	fks := []*storepb.ForeignKeyMetadata{}
	for _, fk := range fkStates {
		fks = append(fks, fk.convertToForeignKeyMetadata())
	}

	return &storepb.TableMetadata{
		Name:        t.name,
		Columns:     columns,
		Indexes:     indexes,
		ForeignKeys: fks,
		Comment:     t.comment,
		Engine:      t.engine,
		Collation:   t.collation,
	}
}

type foreignKeyState struct {
	id                int
	name              string
	columns           []string
	referencedTable   string
	referencedColumns []string
}

func (f *foreignKeyState) convertToForeignKeyMetadata() *storepb.ForeignKeyMetadata {
	return &storepb.ForeignKeyMetadata{
		Name:              f.name,
		Columns:           f.columns,
		ReferencedTable:   f.referencedTable,
		ReferencedColumns: f.referencedColumns,
	}
}

func convertToForeignKeyState(id int, foreignKey *storepb.ForeignKeyMetadata) *foreignKeyState {
	return &foreignKeyState{
		id:                id,
		name:              foreignKey.Name,
		columns:           foreignKey.Columns,
		referencedTable:   foreignKey.ReferencedTable,
		referencedColumns: foreignKey.ReferencedColumns,
	}
}

func (f *foreignKeyState) toString(buf io.StringWriter) error {
	if _, err := buf.WriteString("CONSTRAINT `"); err != nil {
		return err
	}
	if _, err := buf.WriteString(f.name); err != nil {
		return err
	}
	if _, err := buf.WriteString("` FOREIGN KEY ("); err != nil {
		return err
	}
	for i, column := range f.columns {
		if i > 0 {
			if _, err := buf.WriteString(", "); err != nil {
				return err
			}
		}
		if _, err := buf.WriteString("`"); err != nil {
			return err
		}
		if _, err := buf.WriteString(column); err != nil {
			return err
		}
		if _, err := buf.WriteString("`"); err != nil {
			return err
		}
	}
	if _, err := buf.WriteString(") REFERENCES `"); err != nil {
		return err
	}
	if _, err := buf.WriteString(f.referencedTable); err != nil {
		return err
	}
	if _, err := buf.WriteString("` ("); err != nil {
		return err
	}
	for i, column := range f.referencedColumns {
		if i > 0 {
			if _, err := buf.WriteString(", "); err != nil {
				return err
			}
		}
		if _, err := buf.WriteString("`"); err != nil {
			return err
		}
		if _, err := buf.WriteString(column); err != nil {
			return err
		}
		if _, err := buf.WriteString("`"); err != nil {
			return err
		}
	}
	if _, err := buf.WriteString(")"); err != nil {
		return err
	}
	return nil
}

type indexState struct {
	id      int
	name    string
	keys    []string
	lengths []int64
	primary bool
	unique  bool
	tp      string
	comment string
}

func (i *indexState) convertToIndexMetadata() *storepb.IndexMetadata {
	return &storepb.IndexMetadata{
		Name:        i.name,
		Expressions: i.keys,
		Primary:     i.primary,
		Unique:      i.unique,
		Comment:     i.comment,
		KeyLength:   i.lengths,
		// Unsupported, for tests only.
		Visible: true,
		Type:    i.tp,
	}
}

func convertToIndexState(id int, index *storepb.IndexMetadata) *indexState {
	return &indexState{
		id:      id,
		name:    index.Name,
		keys:    index.Expressions,
		primary: index.Primary,
		unique:  index.Unique,
		tp:      index.Type,
		comment: index.Comment,
		lengths: index.KeyLength,
	}
}

func (i *indexState) toString(buf io.StringWriter) error {
	if i.primary {
		if _, err := buf.WriteString("PRIMARY KEY ("); err != nil {
			return err
		}
		for j, key := range i.keys {
			if j > 0 {
				if _, err := buf.WriteString(", "); err != nil {
					return err
				}
			}
			if _, err := buf.WriteString(fmt.Sprintf("`%s`", key)); err != nil {
				return err
			}
			if j < len(i.lengths) && i.lengths[j] > 0 {
				if _, err := buf.WriteString(fmt.Sprintf("(%d)", i.lengths[j])); err != nil {
					return err
				}
			}
		}
		if _, err := buf.WriteString(")"); err != nil {
			return err
		}
	} else {
		if strings.ToUpper(i.tp) == "FULLTEXT" {
			if _, err := buf.WriteString("FULLTEXT KEY "); err != nil {
				return err
			}
		} else if strings.ToUpper(i.tp) == "SPATIAL" {
			if _, err := buf.WriteString("SPATIAL KEY "); err != nil {
				return err
			}
		} else if i.unique {
			if _, err := buf.WriteString("UNIQUE KEY "); err != nil {
				return err
			}
		} else {
			if _, err := buf.WriteString("KEY "); err != nil {
				return err
			}
		}

		if _, err := buf.WriteString(fmt.Sprintf("`%s` (", i.name)); err != nil {
			return err
		}
		for j, key := range i.keys {
			if j > 0 {
				if _, err := buf.WriteString(","); err != nil {
					return err
				}
			}
			if len(key) > 2 && key[0] == '(' && key[len(key)-1] == ')' {
				// Expressions are surrounded by parentheses.
				if _, err := buf.WriteString(key); err != nil {
					return err
				}
			} else {
				if _, err := buf.WriteString(fmt.Sprintf("`%s`", key)); err != nil {
					return err
				}
				if j < len(i.lengths) && i.lengths[j] > 0 {
					if _, err := buf.WriteString(fmt.Sprintf("(%d)", i.lengths[j])); err != nil {
						return err
					}
				}
			}
		}
		if _, err := buf.WriteString(")"); err != nil {
			return err
		}

		if strings.ToUpper(i.tp) == "BTREE" {
			if _, err := buf.WriteString(" USING BTREE"); err != nil {
				return err
			}
		} else if strings.ToUpper(i.tp) == "HASH" {
			if _, err := buf.WriteString(" USING HASH"); err != nil {
				return err
			}
		}
	}

	if i.comment != "" {
		if _, err := buf.WriteString(fmt.Sprintf(" COMMENT '%s'", i.comment)); err != nil {
			return err
		}
	}
	return nil
}

// Currently, our storepb.TablePartitionMetadata is too redundant, we need to convert it to a more compact format.
// In the future, we should update the storepb.TablePartitionMetadata to a more compact format.
type partitionStateWrapper struct {
	tp         storepb.TablePartitionMetadata_Type
	expr       string
	partitions map[string]*partitionState
}

func (p *partitionStateWrapper) hasSubpartitions() (bool, storepb.TablePartitionMetadata_Type, string) {
	for _, partition := range p.partitions {
		if partition.subPartition != nil && len(partition.subPartition.partitions) > 0 {
			return true, partition.subPartition.tp, partition.subPartition.expr
		}
	}

	return false, storepb.TablePartitionMetadata_TYPE_UNSPECIFIED, ""
}

type partitionState struct {
	id           int
	name         string
	value        string
	subPartition *partitionStateWrapper
}

// nolint
func (p *partitionStateWrapper) convertToPartitionMetadata() []*storepb.TablePartitionMetadata {
	partitions := make([]*storepb.TablePartitionMetadata, 0, len(p.partitions))
	partitionStates := make([]*partitionState, 0, len(p.partitions))
	for _, partition := range p.partitions {
		partitionStates = append(partitionStates, partition)
	}
	sort.Slice(partitionStates, func(i, j int) bool {
		return partitionStates[i].id < partitionStates[j].id
	})

	for _, partition := range partitionStates {
		partitions = append(partitions, &storepb.TablePartitionMetadata{
			Name:          partition.name,
			Type:          p.tp,
			Expression:    p.expr,
			Value:         partition.value,
			Subpartitions: partition.subPartition.convertToPartitionMetadata(),
		})
	}

	return partitions
}

func convertToPartitionStateWrapper(partitions []*storepb.TablePartitionMetadata) *partitionStateWrapper {
	if len(partitions) == 0 {
		return nil
	}
	wrapper := &partitionStateWrapper{
		partitions: make(map[string]*partitionState),
	}
	for i, partition := range partitions {
		if i == 0 {
			wrapper.tp = partition.Type
			wrapper.expr = partition.Expression
		}
		partitionState := &partitionState{
			id:    i,
			name:  partition.Name,
			value: partition.Value,
		}
		partitionState.subPartition = convertToPartitionStateWrapper(partition.Subpartitions)
		wrapper.partitions[partition.Name] = partitionState
	}

	return wrapper
}

func (p *partitionStateWrapper) toString(buf io.StringWriter) error {
	// Write version specific comment.
	if _, err := buf.WriteString("/*!50100 PARTITION BY "); err != nil {
		return err
	}

	tp, err := partitionTypeToString(p.tp)
	if err != nil {
		return err
	}

	if _, err := buf.WriteString(tp); err != nil {
		return err
	}

	if _, err := buf.WriteString(fmt.Sprintf(" (%s)", p.expr)); err != nil {
		return err
	}

	// Write subpartition type if any.
	if has, subTp, subExpr := p.hasSubpartitions(); has {
		if _, err := buf.WriteString("\nSUBPARTITION BY "); err != nil {
			return err
		}
		subTp, err := partitionTypeToString(subTp)
		if err != nil {
			return err
		}
		if _, err := buf.WriteString(subTp); err != nil {
			return err
		}
		if _, err := buf.WriteString(fmt.Sprintf(" (%s)", subExpr)); err != nil {
			return err
		}
	}

	parititonSlice := make([]*partitionState, 0, len(p.partitions))
	for _, partition := range p.partitions {
		parititonSlice = append(parititonSlice, partition)
	}
	sort.Slice(parititonSlice, func(i, j int) bool {
		return parititonSlice[i].id < parititonSlice[j].id
	})

	for idx, partition := range parititonSlice {
		prefix := "("
		if idx > 0 {
			prefix = strings.Repeat(" ", 1)
		}
		suffix := ","
		if idx == len(parititonSlice)-1 {
			suffix = ")"
		}

		if _, err := buf.WriteString(fmt.Sprintf("\n%s", prefix)); err != nil {
			return err
		}

		var valuesWrap string
		if strings.EqualFold(partition.value, "MAXVALUE") {
			valuesWrap = "MAXVALUE"
		} else {
			valuesWrap = fmt.Sprintf("(%s)", partition.value)
		}
		if _, err := buf.WriteString(fmt.Sprintf("PARTITION %s VALUES LESS THAN %s", partition.name, valuesWrap)); err != nil {
			return err
		}
		if partition.subPartition == nil {
			if _, err := buf.WriteString(" ENGINE = InnoDB"); err != nil {
				return err
			}
		} else {
			subPartitionSlice := make([]*partitionState, 0, len(partition.subPartition.partitions))
			for _, subPartition := range partition.subPartition.partitions {
				subPartitionSlice = append(subPartitionSlice, subPartition)
			}
			sort.Slice(subPartitionSlice, func(i, j int) bool {
				return subPartitionSlice[i].id < subPartitionSlice[j].id
			})

			for subIdx, subPartition := range subPartitionSlice {
				prefix := " ("
				if subIdx > 0 {
					prefix = strings.Repeat(" ", 2)
				}
				suffix := ","
				if subIdx == len(subPartitionSlice)-1 {
					suffix = ")"
				}
				if _, err := buf.WriteString(fmt.Sprintf("\n%s", prefix)); err != nil {
					return err
				}
				if _, err := buf.WriteString(fmt.Sprintf("SUBPARTITION %s ENGINE = InnoDB", subPartition.name)); err != nil {
					return err
				}
				if _, err := buf.WriteString(suffix); err != nil {
					return err
				}
			}
		}
		if _, err := buf.WriteString(suffix); err != nil {
			return err
		}
	}

	if _, err := buf.WriteString(" */"); err != nil {
		return err
	}

	return nil
}

func partitionTypeToString(tp storepb.TablePartitionMetadata_Type) (string, error) {
	switch tp {
	case storepb.TablePartitionMetadata_RANGE:
		return "RANGE", nil
	case storepb.TablePartitionMetadata_RANGE_COLUMNS:
		return "RANGE COLUMNS", nil
	case storepb.TablePartitionMetadata_LIST:
		return "LIST", nil
	case storepb.TablePartitionMetadata_LIST_COLUMNS:
		return "LIST COLUMNS", nil
	case storepb.TablePartitionMetadata_HASH:
		return "HASH", nil
	case storepb.TablePartitionMetadata_KEY:
		return "KEY", nil
	case storepb.TablePartitionMetadata_LINEAR_HASH:
		return "LINEAR HASH", nil
	case storepb.TablePartitionMetadata_LINEAR_KEY:
		return "LINEAR KEY", nil
	default:
		return "", errors.Errorf("unsupported partition type: %v", tp)
	}
}

type defaultValue interface {
	toString() string
}

type defaultValueNull struct {
}

func (*defaultValueNull) toString() string {
	return "NULL"
}

type defaultValueString struct {
	value string
}

func (d *defaultValueString) toString() string {
	return fmt.Sprintf("'%s'", strings.ReplaceAll(d.value, "'", "''"))
}

type defaultValueExpression struct {
	value string
}

func (d *defaultValueExpression) toString() string {
	return d.value
}

type columnState struct {
	id           int
	name         string
	tp           string
	defaultValue defaultValue
	onUpdate     string
	comment      string
	nullable     bool
}

func (c *columnState) toString(buf io.StringWriter) error {
	if _, err := buf.WriteString(fmt.Sprintf("`%s` %s", c.name, c.tp)); err != nil {
		return err
	}
	if !c.nullable {
		if _, err := buf.WriteString(" NOT NULL"); err != nil {
			return err
		}
	}
	if c.defaultValue != nil {
		_, isDefaultNull := c.defaultValue.(*defaultValueNull)
		dontWriteDefaultNull := isDefaultNull && c.nullable && expressionDefaultOnlyTypes[strings.ToUpper(c.tp)]
		// Some types do not default to NULL, but support default expressions.
		if !dontWriteDefaultNull {
			// todo(zp): refactor column attribute.
			if strings.EqualFold(c.defaultValue.toString(), autoIncrementSymbol) {
				if _, err := buf.WriteString(fmt.Sprintf(" %s", c.defaultValue.toString())); err != nil {
					return err
				}
			} else if strings.Contains(strings.ToUpper(c.defaultValue.toString()), autoRandSymbol) {
				if _, err := buf.WriteString(fmt.Sprintf(" /*T![auto_rand] %s */", c.defaultValue.toString())); err != nil {
					return err
				}
			} else {
				if _, err := buf.WriteString(fmt.Sprintf(" DEFAULT %s", c.defaultValue.toString())); err != nil {
					return err
				}
			}
		}
	}
	if len(c.onUpdate) > 0 {
		if _, err := buf.WriteString(fmt.Sprintf(" ON UPDATE %s", c.onUpdate)); err != nil {
			return err
		}
	}
	if c.comment != "" {
		if _, err := buf.WriteString(fmt.Sprintf(" COMMENT '%s'", c.comment)); err != nil {
			return err
		}
	}
	return nil
}

func (c *columnState) convertToColumnMetadata() *storepb.ColumnMetadata {
	result := &storepb.ColumnMetadata{
		Name:     c.name,
		Type:     c.tp,
		Nullable: c.nullable,
		OnUpdate: c.onUpdate,
		Comment:  c.comment,
	}
	if c.defaultValue != nil {
		switch value := c.defaultValue.(type) {
		case *defaultValueNull:
			result.DefaultValue = &storepb.ColumnMetadata_DefaultNull{DefaultNull: true}
		case *defaultValueString:
			result.DefaultValue = &storepb.ColumnMetadata_Default{Default: wrapperspb.String(value.value)}
		case *defaultValueExpression:
			result.DefaultValue = &storepb.ColumnMetadata_DefaultExpression{DefaultExpression: value.value}
		}
	}
	if result.DefaultValue == nil && c.nullable {
		result.DefaultValue = &storepb.ColumnMetadata_DefaultNull{DefaultNull: true}
	}
	return result
}

func convertToColumnState(id int, column *storepb.ColumnMetadata) *columnState {
	result := &columnState{
		id:       id,
		name:     column.Name,
		tp:       column.Type,
		nullable: column.Nullable,
		onUpdate: normalizeOnUpdate(column.OnUpdate),
		comment:  column.Comment,
	}
	if column.GetDefaultValue() != nil {
		switch value := column.GetDefaultValue().(type) {
		case *storepb.ColumnMetadata_DefaultNull:
			result.defaultValue = &defaultValueNull{}
		case *storepb.ColumnMetadata_Default:
			if value.Default == nil {
				result.defaultValue = &defaultValueNull{}
			} else {
				result.defaultValue = &defaultValueString{value: value.Default.GetValue()}
			}
		case *storepb.ColumnMetadata_DefaultExpression:
			result.defaultValue = &defaultValueExpression{value: value.DefaultExpression}
		}
	}
	return result
}
