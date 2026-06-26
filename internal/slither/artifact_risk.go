package slither

import (
	"strings"
)

func workflowSecurityRisk(rel, text string) (int, []string) {
	lower := strings.ToLower(rel)
	if !strings.HasPrefix(lower, ".github/workflows/") || !(strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")) {
		return 0, nil
	}
	score := 0
	var reasons []string
	privileged := regexpMust(`(?m)^\s*(?:-\s*)?pull_request_target\s*:?\s*$|^\s*on:\s*(?:pull_request_target|\[[^\]]*pull_request_target[^\]]*\])\s*$`).FindStringIndex(text) != nil
	workflowRun := regexpMust(`(?m)^\s*(?:-\s*)?workflow_run\s*:?\s*$|^\s*on:\s*(?:workflow_run|\[[^\]]*workflow_run[^\]]*\])\s*$`).FindStringIndex(text) != nil
	if privileged {
		score += 4
		reasons = append(reasons, "workflow_security:pull_request_target")
	}
	if workflowRun {
		score += 2
		reasons = append(reasons, "workflow_security:workflow_run")
	}
	if (privileged || workflowRun) && strings.Contains(text, "actions/checkout@") && strings.Contains(text, "github.event.pull_request.") {
		score += 6
		reasons = append(reasons, "workflow_security:privileged_untrusted_checkout")
	}
	if count := len(regexpMust(`(?m)^\s*permissions:\s*write-all\s*$|^\s*(contents|pull-requests|issues|actions|id-token):\s*write\s*$`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "workflow_security:write_permissions:"+itoa(count))
	}
	if count := len(regexpMust(`(?s)\brun:\s*(\||>|[^\n]*).{0,800}\$\{\{\s*github\.event\.(pull_request|issue|comment|review|discussion|head_commit)\.`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "workflow_security:inline_event_context:"+itoa(count))
	}
	if count := countUnpinnedActions(text); count > 0 {
		score += min(count, 5)
		reasons = append(reasons, "workflow_security:unpinned_actions:"+itoa(count))
	}
	if count := len(regexpMust(`(?i)\b(curl|wget)\b[^\n|;]{0,200}\|\s*(sudo\s+)?(sh|bash)\b`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*4, 8)
		reasons = append(reasons, "workflow_security:curl_pipe_shell:"+itoa(count))
	}
	return score, reasons
}

func countUnpinnedActions(text string) int {
	count := 0
	for _, match := range regexpMust(`(?m)^\s*-?\s*uses:\s*([^@\s]+)@([^\s#]+)`).FindAllStringSubmatch(text, -1) {
		if len(match) < 3 || strings.HasPrefix(match[1], "./") || strings.HasPrefix(match[1], "docker://") {
			continue
		}
		if regexpMust(`^[0-9a-fA-F]{40}$`).FindStringIndex(match[2]) == nil {
			count++
		}
	}
	return count
}

func migrationSafetyRisk(rel, text string) (int, []string) {
	lower := strings.ToLower(rel)
	if !(strings.Contains(lower, "migration") || strings.Contains(lower, "database") || strings.Contains(lower, "schema") || strings.Contains(lower, "db/migrate")) {
		return 0, nil
	}
	score := 0
	var reasons []string
	if count := len(regexpMust(`(?is)\b(DROP\s+TABLE|DROP\s+COLUMN|TRUNCATE\s+TABLE|ALTER\s+TABLE\b.{0,160}\bDROP\b)`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*4, 8)
		reasons = append(reasons, "migration_safety:destructive_ops:"+itoa(count))
		if regexpMust(`(?i)\b(down|rollback|revert|undo|def\s+down|down\(\s*\))\b`).FindStringIndex(text) == nil {
			score += 4
			reasons = append(reasons, "migration_safety:no_rollback_cue")
		}
	}
	if count := countSQLStatementsWithoutWhere(text, `(?is)\bDELETE\s+FROM\s+\S+[^;]{0,240};`); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "migration_safety:delete_without_where:"+itoa(count))
	}
	if count := countSQLStatementsWithoutWhere(text, `(?is)\bUPDATE\s+\S+\s+SET\b[^;]{0,240};`); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "migration_safety:update_without_where:"+itoa(count))
	}
	if count := countBlockingIndexStatements(text); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "migration_safety:blocking_index:"+itoa(count))
	}
	return score, reasons
}

func countBlockingIndexStatements(text string) int {
	count := 0
	for _, statement := range regexpMust(`(?is)\bCREATE\s+(UNIQUE\s+)?INDEX\s+[^;]{0,240};?`).FindAllString(text, -1) {
		if !strings.Contains(strings.ToLower(statement), "concurrently") {
			count++
		}
	}
	return count
}

func countSQLStatementsWithoutWhere(text, expr string) int {
	count := 0
	for _, statement := range regexpMust(expr).FindAllString(text, -1) {
		if !strings.Contains(strings.ToLower(statement), " where ") {
			count++
		}
	}
	return count
}

func containerBuildRisk(rel, text string) (int, []string) {
	name := strings.ToLower(filepathBase(rel))
	if !(name == "dockerfile" || name == "containerfile" || strings.HasPrefix(name, "dockerfile.") || strings.HasSuffix(name, ".dockerfile")) {
		return 0, nil
	}
	score := 0
	var reasons []string
	if count := len(regexpMust(`(?mi)^FROM\s+(?:--platform=\S+\s+)?(?:[^@\s:]+|[^@\s]+:latest)\s*$`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "container_build:floating_base:"+itoa(count))
	}
	if regexpMust(`(?mi)^USER\s+`).FindStringIndex(text) == nil {
		score += 3
		reasons = append(reasons, "container_build:no_user")
	} else if user := lastDockerfileUser(text); user == "0" || user == "root" || strings.HasPrefix(user, "root:") {
		score += 3
		reasons = append(reasons, "container_build:root_user")
	}
	for _, apt := range dockerfileAptRisks(text) {
		score += apt.score
		reasons = append(reasons, apt.reason)
	}
	if count := len(regexpMust(`(?i)\b(curl|wget)\b[^|;]{0,240}\|\s*(sudo\s+)?(sh|bash)\b`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*4, 8)
		reasons = append(reasons, "container_build:curl_pipe_shell:"+itoa(count))
	}
	if count := len(regexpMust(`(?mi)^ADD\s+(--\S+\s+)*https?://`).FindAllStringIndex(text, -1)); count > 0 && !strings.Contains(strings.ToLower(text), "checksum=") {
		score += min(count*3, 6)
		reasons = append(reasons, "container_build:remote_add_no_checksum:"+itoa(count))
	}
	if count := len(regexpMust(`(?mi)^(ARG|ENV)\s+.*(token|secret|password|passwd|pwd|api[_-]?key|client[_-]?secret)`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "container_build:secret_arg_or_env:"+itoa(count))
	}
	return score, reasons
}

func lastDockerfileUser(text string) string {
	user := ""
	for _, match := range regexpMust(`(?mi)^USER\s+(.+)$`).FindAllStringSubmatch(text, -1) {
		if len(match) >= 2 {
			user = strings.ToLower(strings.TrimSpace(match[1]))
		}
	}
	return user
}

type scoredReason struct {
	score  int
	reason string
}

func dockerfileAptRisks(text string) []scoredReason {
	var out []scoredReason
	aptUpdateSplit := 0
	aptInstallNoCleanup := 0
	aptInstallNoRecommends := 0
	for _, line := range dockerfileInstructions(text) {
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "run ") {
			continue
		}
		if strings.Contains(lower, "apt-get update") && !strings.Contains(lower, "apt-get install") {
			aptUpdateSplit++
		}
		if strings.Contains(lower, "apt-get install") {
			if !strings.Contains(lower, "--no-install-recommends") {
				aptInstallNoRecommends++
			}
			if !strings.Contains(lower, "/var/lib/apt/lists") {
				aptInstallNoCleanup++
			}
		}
	}
	if aptUpdateSplit > 0 {
		out = append(out, scoredReason{score: min(aptUpdateSplit*2, 4), reason: "container_build:apt_update_split:" + itoa(aptUpdateSplit)})
	}
	if aptInstallNoRecommends > 0 {
		out = append(out, scoredReason{score: min(aptInstallNoRecommends, 3), reason: "container_build:apt_install_recommends:" + itoa(aptInstallNoRecommends)})
	}
	if aptInstallNoCleanup > 0 {
		out = append(out, scoredReason{score: min(aptInstallNoCleanup*2, 4), reason: "container_build:apt_cache_not_cleaned:" + itoa(aptInstallNoCleanup)})
	}
	return out
}

func dockerfileInstructions(text string) []string {
	var instructions []string
	current := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if current != "" {
			current += " " + trimmed
		} else {
			current = trimmed
		}
		if strings.HasSuffix(current, `\`) {
			current = strings.TrimSpace(strings.TrimSuffix(current, `\`))
			continue
		}
		instructions = append(instructions, current)
		current = ""
	}
	if current != "" {
		instructions = append(instructions, current)
	}
	return instructions
}

func kubernetesSecurityRisk(rel, text string) (int, []string) {
	lower := strings.ToLower(rel)
	if !(strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")) {
		return 0, nil
	}
	pathHint := strings.Contains(lower, "k8s") || strings.Contains(lower, "kubernetes") || strings.Contains(lower, "manifest") || strings.Contains(lower, "helm") || strings.Contains(lower, "chart") || strings.Contains(lower, "deploy")
	apiHint := regexpMust(`(?m)^\s*apiVersion:\s*(apps/|batch/|networking\.k8s\.io/|rbac\.authorization\.k8s\.io/|v1\b)`).FindStringIndex(text) != nil
	kindHint := regexpMust(`(?m)^\s*kind:\s*(Pod|Deployment|StatefulSet|DaemonSet|Job|CronJob|ReplicaSet|Service|Ingress)\s*$`).FindStringIndex(text) != nil
	if !(pathHint && (apiHint || kindHint) || apiHint && kindHint) {
		return 0, nil
	}
	score := 0
	var reasons []string
	if count := len(regexpMust(`(?m)^\s*host(Network|PID|IPC):\s*true\s*$`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*4, 8)
		reasons = append(reasons, "kubernetes_security:host_namespace:"+itoa(count))
	}
	if count := len(regexpMust(`(?m)^\s*privileged:\s*true\s*$`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*5, 10)
		reasons = append(reasons, "kubernetes_security:privileged:"+itoa(count))
	}
	if count := len(regexpMust(`(?m)^\s*hostPath:\s*$`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*4, 8)
		reasons = append(reasons, "kubernetes_security:host_path:"+itoa(count))
	}
	if count := len(regexpMust(`(?m)^\s*hostPort:\s*[1-9][0-9]*\s*$`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "kubernetes_security:host_port:"+itoa(count))
	}
	if count := len(regexpMust(`(?m)^\s*allowPrivilegeEscalation:\s*true\s*$`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "kubernetes_security:allow_privilege_escalation:"+itoa(count))
	}
	if count := len(regexpMust(`(?m)^\s*runAsUser:\s*0\s*$`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "kubernetes_security:run_as_root:"+itoa(count))
	}
	if count := len(regexpMust(`(?m)^\s*add:\s*$|^\s*-\s*(SYS_ADMIN|NET_ADMIN|ALL)\s*$`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "kubernetes_security:capabilities_add:"+itoa(count))
	}
	if regexpMust(`(?m)^\s*kind:\s*(Pod|Deployment|StatefulSet|DaemonSet|Job|CronJob|ReplicaSet)\s*$`).FindStringIndex(text) != nil {
		if !strings.Contains(text, "runAsNonRoot:") {
			score += 2
			reasons = append(reasons, "kubernetes_security:missing_run_as_non_root")
		}
		if !strings.Contains(text, "seccompProfile:") {
			score += 2
			reasons = append(reasons, "kubernetes_security:missing_seccomp_profile")
		}
		if !strings.Contains(text, "readOnlyRootFilesystem:") {
			score++
			reasons = append(reasons, "kubernetes_security:missing_readonly_rootfs")
		}
	}
	return score, reasons
}

func terraformSecurityRisk(rel, text string) (int, []string) {
	lower := strings.ToLower(rel)
	if !(strings.HasSuffix(lower, ".tf") || strings.HasSuffix(lower, ".tfvars")) {
		return 0, nil
	}
	score := 0
	var reasons []string
	if count := len(regexpMust(`cidr_blocks\s*=\s*\[[^\]]*"0\.0\.0\.0/0"[^\]]*\]|ipv6_cidr_blocks\s*=\s*\[[^\]]*"::/0"[^\]]*\]`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*4, 8)
		reasons = append(reasons, "terraform_security:public_cidr:"+itoa(count))
	}
	if count := len(regexpMust(`(?s)(ingress\s*\{|resource\s+"aws_security_group_rule"[^}]*type\s*=\s*"ingress")[^}]{0,800}(from_port\s*=\s*(22|3389)|to_port\s*=\s*(22|3389))[^}]{0,800}("0\.0\.0\.0/0"|"::/0")`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*5, 10)
		reasons = append(reasons, "terraform_security:broad_admin_port:"+itoa(count))
	}
	if count := len(regexpMust(`(?s)actions?\s*=\s*\[[^\]]*"\*"[^\]]*\].{0,500}resources?\s*=\s*\[[^\]]*"\*"[^\]]*\]|"Action"\s*:\s*("\*"|\[[^\]]*"\*"[^\]]*\]).{0,500}"Resource"\s*:\s*("\*"|\[[^\]]*"\*"[^\]]*\])`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*6, 12)
		reasons = append(reasons, "terraform_security:iam_wildcard:"+itoa(count))
	}
	if count := len(regexpMust(`acl\s*=\s*"(public-read|public-read-write|website)"`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*4, 8)
		reasons = append(reasons, "terraform_security:public_s3_acl:"+itoa(count))
	}
	if count := len(regexpMust(`(?s)resource\s+"aws_s3_bucket_public_access_block"[^}]*\{[^}]{0,1000}(block_public_acls|block_public_policy|ignore_public_acls|restrict_public_buckets)\s*=\s*false`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*4, 8)
		reasons = append(reasons, "terraform_security:s3_public_block_disabled:"+itoa(count))
	}
	if count := len(regexpMust(`publicly_accessible\s*=\s*true`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*4, 8)
		reasons = append(reasons, "terraform_security:public_database:"+itoa(count))
	}
	if count := len(regexpMust(`\b(encrypted|storage_encrypted|server_side_encryption|kms_key_id)\s*=\s*(false|null|""|0)\b`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 9)
		reasons = append(reasons, "terraform_security:disabled_encryption:"+itoa(count))
	}
	return score, reasons
}

func sdkDXRisk(rel, text string) (int, []string) {
	lower := strings.ToLower(rel)
	score := 0
	var reasons []string
	if lower == "readme.md" || strings.Contains(lower, "quickstart") || strings.Contains(lower, "example") || strings.Contains(lower, "changelog") {
		score++
		reasons = append(reasons, "sdk_dx:surface_path")
	}
	if !(strings.Contains(lower, "client") || strings.Contains(lower, "sdk") || strings.Contains(lower, "api")) {
		return score, reasons
	}
	if isArchitectureSource(rel) {
		score++
		reasons = append(reasons, "sdk_dx:source_path")
	}
	if strings.HasSuffix(lower, ".go") {
		clientMethods := regexpMust(`func\s+\([^)]*\*Client[^)]*\)\s+([A-Z]\w+)\s*\(([^)]*)\)`).FindAllStringSubmatch(text, -1)
		stems := map[string]map[string]bool{}
		for _, match := range clientMethods {
			if len(match) >= 3 && !isSDKContextExemptMethod(match[1]) && !strings.Contains(match[2], "context.Context") {
				score += 3
				reasons = append(reasons, "sdk_dx:contextless_client_method:"+match[1])
			}
			stem := sdkMethodStem(match[1])
			if stems[stem] == nil {
				stems[stem] = map[string]bool{}
			}
			stems[stem][match[1]] = true
		}
		duplicateGroups := 0
		for _, names := range stems {
			if len(names) >= 2 {
				duplicateGroups++
			}
		}
		if duplicateGroups > 0 {
			score += min(duplicateGroups*3, 6)
			reasons = append(reasons, "sdk_dx:duplicate_client_method_groups:"+itoa(duplicateGroups))
		}
	}
	if strings.Contains(text, "panic(") && !isTestFile(rel) {
		score += 3
		reasons = append(reasons, "sdk_dx:panic_in_non_test")
	}
	if strings.Contains(text, "type Provider interface") || strings.Contains(text, "WithMockProvider") || strings.Contains(strings.ToLower(text), "mock") {
		score++
		reasons = append(reasons, "sdk_dx:testability_hook")
	}
	if regexpMust(`\b(Len|LastMessage|HasSystem|FallbackModel|QuickText)\s*\(`).FindStringIndex(text) != nil {
		score++
		reasons = append(reasons, "sdk_dx:introspection_or_quick_api")
	}
	return score, reasons
}

func isSDKContextExemptMethod(name string) bool {
	switch name {
	case "New", "Model", "FallbackModel":
		return true
	default:
		return false
	}
}

func sdkMethodStem(name string) string {
	for _, suffix := range []string{"WithContext", "Text", "Generate", "Stream"} {
		if strings.HasSuffix(name, suffix) {
			stem := strings.TrimSuffix(name, suffix)
			if stem == "" {
				return name
			}
			return stem
		}
	}
	return name
}

func openAPIContractRisk(rel, text string) (int, []string) {
	lower := strings.ToLower(rel)
	hasSpecPath := strings.Contains(lower, "openapi") || strings.Contains(lower, "swagger")
	hasSpecMarker := regexpMust(`(?mi)^\s*openapi\s*:\s*['"]?\d|"\s*openapi\s*"\s*:\s*"\d`).FindStringIndex(text) != nil
	if !hasSpecPath && !hasSpecMarker {
		return 0, nil
	}
	operations := len(regexpMust(`(?mi)^\s+(get|put|post|delete|options|head|patch|trace):\s*$|"(get|put|post|delete|options|head|patch|trace)"\s*:`).FindAllStringIndex(text, -1))
	if operations == 0 {
		return 0, nil
	}
	score := 0
	var reasons []string
	hasSecurity := regexpMust(`(?m)^\s*security:\s*|"\s*security\s*"\s*:`).FindStringIndex(text) != nil
	hasSchemes := regexpMust(`\b(securitySchemes|securityDefinitions)\b`).FindStringIndex(text) != nil
	if !hasSecurity {
		if hasSchemes {
			score += 3
			reasons = append(reasons, "openapi_contract:security_schemes_not_required")
		} else {
			score += 5
			reasons = append(reasons, "openapi_contract:missing_security_contract")
		}
	} else if !hasSchemes {
		score += 2
		reasons = append(reasons, "openapi_contract:security_without_schemes")
	}
	if count := len(regexpMust(`(?m)^\s*security:\s*\[\s*\]\s*$|^\s*-\s*\{\s*\}\s*$|"security"\s*:\s*\[\s*(\{\s*\})?\s*\]`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*4, 8)
		reasons = append(reasons, "openapi_contract:anonymous_security:"+itoa(count))
	}
	if count := countExternalHTTPServerURLs(text); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "openapi_contract:insecure_server_url:"+itoa(count))
	}
	if regexpMust(`(?is)\btype:\s*http\b.{0,120}\bscheme:\s*basic\b|"type"\s*:\s*"http".{0,160}"scheme"\s*:\s*"basic"`).FindStringIndex(text) != nil {
		score += 2
		reasons = append(reasons, "openapi_contract:basic_auth_scheme")
	}
	if regexpMust(`(?m)^\s*implicit:\s*$|"implicit"\s*:`).FindStringIndex(text) != nil {
		score += 2
		reasons = append(reasons, "openapi_contract:implicit_oauth_flow")
	}
	return score, reasons
}

func countExternalHTTPServerURLs(text string) int {
	count := 0
	for _, match := range regexpMust(`(?mi)(url:\s*["']?|\"url\"\s*:\s*\")http://([^"'\s]+)`).FindAllStringSubmatch(text, -1) {
		if len(match) < 3 {
			continue
		}
		host := strings.ToLower(match[2])
		if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") || strings.HasPrefix(host, "[::1]") {
			continue
		}
		count++
	}
	return count
}

func corsSecurityRisk(rel, text string) (int, []string) {
	if !strings.Contains(strings.ToLower(text), "cors") && !strings.Contains(strings.ToLower(text), "access-control-allow-") {
		return 0, nil
	}
	score := 0
	var reasons []string
	wildcardOrigin := regexpMust(`(?i)access-control-allow-origin['"]?\s*[:=,]\s*['"]?\*|alloworigin\s*\(\s*['"]\*['"]|origin\s*:\s*['"]\*['"]`).FindStringIndex(text) != nil
	allowCredentials := regexpMust(`(?i)access-control-allow-credentials['"]?\s*[:=,]\s*['"]?true\b|allowcredentials\s*\(\s*true\s*\)|credentials\s*:\s*true`).FindStringIndex(text) != nil
	if wildcardOrigin && allowCredentials {
		score += 8
		reasons = append(reasons, "cors_security:wildcard_origin_with_credentials")
	} else if wildcardOrigin {
		score += 3
		reasons = append(reasons, "cors_security:wildcard_origin:1")
	}
	reflectedOrigin := len(regexpMust(`(?is)(access-control-allow-origin|alloworigin|origin\s*:).{0,160}(request\.headers?\[['"]origin['"]\]|req\.headers?\.origin|r\.Header\.Get\(['"]Origin['"]\)|request\.headers\.get\(['"]origin['"]\)|\$?_SERVER\[['"]HTTP_ORIGIN['"]\])`).FindAllStringIndex(text, -1))
	if reflectedOrigin > 0 {
		score += min(reflectedOrigin*4, 8)
		reasons = append(reasons, "cors_security:reflected_origin:"+itoa(reflectedOrigin))
		if allowCredentials {
			score += 3
			reasons = append(reasons, "cors_security:reflected_origin_with_credentials")
		}
		if regexpMust(`(?i)\bvary\b.{0,80}\borigin\b|origin.{0,80}\bvary\b`).FindStringIndex(text) == nil {
			score += 2
			reasons = append(reasons, "cors_security:missing_vary_origin")
		}
	}
	if count := len(regexpMust(`(?i)access-control-allow-methods['"]?\s*[:=,]\s*['"]?\*|allowmethods\s*\(\s*['"]\*['"]|methods\s*:\s*['"]\*['"]`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "cors_security:wildcard_methods:"+itoa(count))
	}
	if count := len(regexpMust(`(?i)access-control-allow-headers['"]?\s*[:=,]\s*['"]?\*|allowheaders\s*\(\s*['"]\*['"]|allowedheaders\s*:\s*['"]\*['"]`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "cors_security:wildcard_headers:"+itoa(count))
	}
	if count := len(regexpMust(`(?i)access-control-max-age['"]?\s*[:=,]\s*['"]?(86400|[1-9][0-9]{5,})\b|maxage\s*\(\s*(86400|[1-9][0-9]{5,})\s*\)`).FindAllStringIndex(text, -1)); count > 0 && (wildcardOrigin || reflectedOrigin > 0) {
		score++
		reasons = append(reasons, "cors_security:long_preflight_cache_with_broad_policy")
	}
	return score, reasons
}

func cookieSecurityRisk(rel, text string) (int, []string) {
	if regexpMust(`(?i)(set-cookie|http\.SetCookie|\.setcookie\s*\(|\bsetcookie\s*\(|\bres\.cookie\s*\(|response\.set_cookie\s*\(|cookies\.set\s*\()`).FindStringIndex(text) == nil {
		return 0, nil
	}
	score := 0
	var reasons []string
	if regexpMust(`(?i)(session|sess|auth|token|jwt|csrf|xsrf)`).FindStringIndex(text) != nil {
		if regexpMust(`(?i)\bhttponly\b\s*[:=]?\s*(true|1)?`).FindStringIndex(text) == nil {
			score += 3
			reasons = append(reasons, "cookie_security:sensitive_cookie_missing_httponly")
		}
		if regexpMust(`(?i)\bsecure\b\s*[:=]?\s*(true|1)?`).FindStringIndex(text) == nil || regexpMust(`(?i)\bsecure\b\s*[:=]\s*(false|0)`).FindStringIndex(text) != nil {
			score += 3
			reasons = append(reasons, "cookie_security:sensitive_cookie_missing_secure")
		}
		if regexpMust(`(?i)samesite|same_site|same-site`).FindStringIndex(text) == nil {
			score += 2
			reasons = append(reasons, "cookie_security:sensitive_cookie_missing_samesite")
		}
	}
	if regexpMust(`(?i)samesite\s*[:=]\s*['"]?none\b`).FindStringIndex(text) != nil && regexpMust(`(?i)\bsecure\b\s*[:=]?\s*(true|1)`).FindStringIndex(text) == nil {
		score += 6
		reasons = append(reasons, "cookie_security:samesite_none_without_secure")
	}
	if regexpMust(`(?i)\bpartitioned\b`).FindStringIndex(text) != nil && regexpMust(`(?i)\bsecure\b\s*[:=]?\s*(true|1)`).FindStringIndex(text) == nil {
		score += 4
		reasons = append(reasons, "cookie_security:partitioned_without_secure")
	}
	if regexpMust(`(?i)__secure-[A-Za-z0-9_.-]*`).FindStringIndex(text) != nil && regexpMust(`(?i)\bsecure\b\s*[:=]?\s*(true|1)`).FindStringIndex(text) == nil {
		score += 5
		reasons = append(reasons, "cookie_security:secure_prefix_missing_secure")
	}
	if regexpMust(`(?i)__host-[A-Za-z0-9_.-]*`).FindStringIndex(text) != nil {
		if regexpMust(`(?i)\bsecure\b\s*[:=]?\s*(true|1)`).FindStringIndex(text) == nil {
			score += 5
			reasons = append(reasons, "cookie_security:host_prefix_missing_secure")
		}
		if regexpMust(`(?i)\bpath\s*[:=]\s*['"]?/`).FindStringIndex(text) == nil {
			score += 4
			reasons = append(reasons, "cookie_security:host_prefix_missing_path_root")
		}
		if regexpMust(`(?i)\bdomain\s*[:=]`).FindStringIndex(text) != nil {
			score += 4
			reasons = append(reasons, "cookie_security:host_prefix_has_domain")
		}
	}
	if regexpMust(`(?i)\bdomain\s*[:=]\s*['"]?\.`).FindStringIndex(text) != nil {
		score += 2
		reasons = append(reasons, "cookie_security:broad_domain_scope")
	}
	return min(score, 20), uniqueStrings(reasons)
}

func uniqueStrings(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}
