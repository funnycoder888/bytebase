<template>
  <NButton type="primary" size="large" @click="toExportCenter">
    <template #icon>
      <heroicons-outline:download class="w-5 h-5" />
    </template>

    {{ $t("export-center.self") }}
  </NButton>
</template>

<script setup lang="ts">
import { NButton } from "naive-ui";
import { useRouter } from "vue-router";
import { useIssueContext } from "@/components/IssueV1";
import { WORKSPACE_ROUTE_EXPORT_CENTER } from "@/router/dashboard/workspaceRoutes";
import { extractIssueUID } from "@/utils";

const router = useRouter();
const { issue } = useIssueContext();

const toExportCenter = () => {
  router.push({
    name: WORKSPACE_ROUTE_EXPORT_CENTER,
    hash: `#${extractIssueUID(issue.value.name)}`,
  });
};
</script>
