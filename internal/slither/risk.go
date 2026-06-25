package slither

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

//go:embed patterns/triage_patterns.json
var embeddedTriagePatterns []byte

type contentPattern struct {
	ID         string
	Pattern    *regexp2.Regexp
	Weight     int
	MaxMatches int
	Layer      string
}

type scoringPatterns struct {
	PathTerms       []fallbackTerm
	ContentPatterns []contentPattern
	Source          string
}

var fallbackPathTerms = []fallbackTerm{
	{Term: "auth", Weight: 5, Layer: "path-risk"},
	{Term: "security", Weight: 5, Layer: "path-risk"},
	{Term: "crypto", Weight: 5, Layer: "path-risk"},
	{Term: "permission", Weight: 5, Layer: "path-risk"},
	{Term: "payment", Weight: 5, Layer: "path-risk"},
	{Term: "billing", Weight: 5, Layer: "path-risk"},
	{Term: "secret", Weight: 5, Layer: "path-risk"},
	{Term: "token", Weight: 4, Layer: "path-risk"},
	{Term: "credential", Weight: 4, Layer: "path-risk"},
	{Term: "migration", Weight: 4, Layer: "path-risk"},
	{Term: "database", Weight: 4, Layer: "path-risk"},
	{Term: "db", Weight: 3, Layer: "path-risk"},
	{Term: "workflow", Weight: 3, Layer: "path-risk"},
	{Term: "agent", Weight: 3, Layer: "path-risk"},
	{Term: "config", Weight: 3, Layer: "path-risk"},
	{Term: "deploy", Weight: 3, Layer: "path-risk"},
	{Term: "router", Weight: 3, Layer: "path-risk"},
	{Term: "handler", Weight: 3, Layer: "path-risk"},
	{Term: "main.go", Weight: 4, Layer: "path-risk"},
	{Term: "types.go", Weight: 2, Layer: "path-risk"},
	{Term: "go.mod", Weight: 4, Layer: "path-risk"},
	{Term: "go.sum", Weight: 3, Layer: "path-risk"},
	{Term: "readme", Weight: 2, Layer: "path-risk"},
	{Term: "cache", Weight: 3, Layer: "path-risk"},
	{Term: "env", Weight: 2, Layer: "path-risk"},
	{Term: "utils", Weight: 2, Layer: "path-risk"},
	{Term: "helpers", Weight: 2, Layer: "path-risk"},
}

var fallbackContentPatterns = []contentPattern{
	pattern("todo", `\bTODO\b`, 2, 5, "work-marker"),
	pattern("fixme", `\bFIXME\b`, 2, 5, "work-marker"),
	pattern("panic_call", `\bpanic\s*\(`, 3, 5, "content-risk"),
	pattern("process_exit", `\b(os\.Exit|process\.exit|System\.exit)\s*\(`, 2, 4, "content-risk"),
	pattern("shell_boundary", `\b(exec\.Command|subprocess\.run|child_process\.|shell_exec|passthru|system\s*\()`, 2, 4, "content-risk"),
	pattern("dynamic_eval", `\b(eval|Function)\s*\(`, 3, 3, "content-risk"),
	pattern("browser_injection", `\b(innerHTML|dangerouslySetInnerHTML)\b`, 3, 3, "content-risk"),
	pattern("open_redirect_request_target", `(?is)\b(?:res\.redirect|res\.location|redirect|RedirectResponse|NextResponse\.redirect|response\.sendRedirect|Response\.Redirect|redirect_to|header\s*\(\s*["']Location:)\s*\([^;\n]{0,240}\b(?:req(?:uest)?\.(?:query|body|params)|request\.(?:args|form|GET|POST|query_params|nextUrl\.searchParams)|params\s*\[:(?:url|next|redirect|return_to|returnUrl)\]|searchParams\.get\s*\(\s*["'](?:url|next|redirect|return|returnTo|return_to|continue|to)["']|getParameter\s*\(\s*["'](?:url|next|redirect|return|returnTo|return_to|continue|to)["'])`, 5, 5, "content-risk"),
	pattern("open_forward_request_target", `(?is)\b(?:getRequestDispatcher|forward)\s*\([^;\n]{0,200}\b(?:request\.getParameter\s*\(\s*["'](?:fwd|forward|url|next|redirect)["']|req(?:uest)?\.(?:query|body|params)|request\.(?:args|form|GET|POST|query_params))`, 5, 5, "content-risk"),
	pattern("csrf_framework_disabled_or_exempt", `(?is)(?:@c[s]rf_exempt\b|\bc[s]rf\s*\([^)]*\)\s*\.\s*disable\s*\(|\bc[s]rf\s*:\s*false\b|\bc[s]rfProtection\s*:\s*false\b|\bignoreC[S]RF\b|\bC[S]RF_COOKIE_SECURE\s*=\s*False\b)`, 5, 5, "content-risk"),
	pattern("csrf_token_in_url", `(?i)(?:[?&](?:c[s]rf|x[s]rf|c[s]rf[_-]?token|authenticity_token)=|URLSearchParams\s*\([^\n;]{0,180}(?:c[s]rf|x[s]rf|authenticity_token))`, 4, 5, "content-risk"),
	pattern("csrf_credentialed_state_change_client", `(?is)(?:\bfetch\s*\([^;\n]{0,420}\bmethod\s*:\s*["'](?:POST|PUT|PATCH|DELETE)["'][^;\n]{0,420}\bcredentials\s*:\s*["']include["']|\baxios\.(?:post|put|patch|delete|request)\s*\([^;\n]{0,420}\bwithCredentials\s*:\s*true)`, 3, 5, "content-risk"),
	pattern("csrf_broad_allowed_origins", `\ballowedOrigins\s*:\s*\[[^\]]*["']\*["']`, 4, 5, "content-risk"),
	pattern("idor_request_param_object_lookup", `(?is)\b(?:findById|findUnique|findFirst|findOne|get_object_or_404|objects\.get|Model\.find|Project\.find|User\.find)\s*\([^;\n]{0,260}\b(?:req(?:uest)?\.(?:params|query|body)\.(?:id|userId|user_id|accountId|account_id|projectId|project_id|documentId|document_id)|params\.(?:id|userId|user_id|accountId|account_id|projectId|project_id|documentId|document_id)|searchParams\.get\s*\(\s*["'](?:id|userId|accountId|projectId|documentId)["']|request\.(?:args|form|json|query_params)\s*\[\s*["'](?:id|user_id|account_id|project_id|document_id)["'])`, 5, 5, "content-risk"),
	pattern("idor_sql_request_id_lookup", `(?is)\b(?:SELECT|UPDATE|DELETE)\b[^;\n]{0,260}\bWHERE\s+(?:\w+\.)?(?:id|user_id|account_id|project_id|document_id)\s*=\s*[^;\n]{0,180}\b(?:req(?:uest)?\.(?:params|query|body)|params\.|request\.(?:args|form|json|query_params))`, 5, 5, "content-risk"),
	pattern("idor_hidden_object_id_field", `(?is)<input\b[^>]{0,260}\btype\s*=\s*["']hidden["'][^>]{0,260}\bname\s*=\s*["'](?:user_id|account_id|project_id|document_id|owner_id|tenant_id)["']`, 4, 5, "content-risk"),
	pattern("idor_graphql_id_variable_mutation", `(?is)\bmutation\b[^;\n]{0,420}\$\w*(?:Id|ID|Key|Keys)\s*:\s*(?:\[\s*)?ID!?|\bvariables\s*[:=]\s*\{[^}\n]{0,360}\b(?:userId|accountId|projectId|documentId|reportKeys)\b`, 4, 5, "content-risk"),
	pattern("mass_assignment_request_body_write", `(?is)\b(?:new\s+[A-Z]\w*|[A-Z]\w*\.(?:create|update|findByIdAndUpdate|updateOne)|\w+\.(?:create|update|upsert)|Object\.assign)\s*\([^;\n]{0,320}\b(?:r[e]q\.body|request\.(?:json|data)|request\.get_json\s*\(|request\.data|await\s+request\.json\s*\(\s*\))`, 5, 5, "content-risk"),
	pattern("mass_assignment_request_body_spread_or_set", `(?is)\b(?:create|update|findByIdAndUpdate|updateOne|updateMany)\s*\([^;\n]{0,320}(?:\.\.\.\s*r[e]q\.body|data\s*:\s*r[e]q\.body|\$set\s*:\s*r[e]q\.body|body\s*:\s*await\s+request\.json\s*\(\s*\))`, 5, 5, "content-risk"),
	pattern("mass_assignment_python_request_dict_expand", `(?is)\b(?:payload|data|body)\s*=\s*(?:await\s+request\.json\s*\(\s*\)|request\.get_json\s*\(\s*\)|request\.data)[^;]{0,700}\b(?:[A-Z]\w+\s*\(\s*\*\*\s*(?:payload|data|body)|\w+\.(?:update|create)\s*\(\s*\*\*\s*(?:payload|data|body))`, 5, 5, "content-risk"),
	pattern("mass_assignment_django_all_fields", `(?is)\bclass\s+\w+Form\b[^;]{0,900}\bfields\s*=\s*["']__all__["']`, 4, 5, "content-risk"),
	pattern("mass_assignment_laravel_unguarded", `protected\s+\$guarded\s*=\s*(?:array\s*\(\s*\)|\[\s*\])`, 4, 5, "content-risk"),
	pattern("nosql_request_object_query", `(?is)\b(?:find|findOne|findByIdAndUpdate|updateOne|updateMany|deleteOne|deleteMany|aggregate)\s*\([^;\n]{0,180}\b(?:req(?:uest)?\.(?:query|body|params)|ctx\.(?:query|request\.body)|request\.(?:args|form|json|query_params))\b`, 5, 5, "content-risk"),
	pattern("nosql_request_spread_or_merge", `(?is)\b(?:find|findOne|aggregate|updateOne|updateMany)\s*\([^;\n]{0,220}(?:\.\.\.\s*(?:req(?:uest)?\.(?:query|body|params)|ctx\.(?:query|request\.body))|Object\.assign\s*\([^;\n]{0,120}\b(?:req(?:uest)?\.(?:query|body|params)|ctx\.(?:query|request\.body)))`, 5, 5, "content-risk"),
	pattern("nosql_operator_or_eval_surface", `(?:["']\$(?:where|regex|expr|ne|gt|gte|lt|lte|nin|or|and)["']|\b(?:db\.eval|\$where)\b)`, 4, 5, "content-risk"),
	pattern("ssrf_user_controlled_url_fetch", `(?is)\b(?:fetch|axios\.(?:get|post|request)|got|request|http\.Get|http\.Post|requests\.(?:get|post|request)|httpx\.(?:get|post|request)|urllib\.request\.urlopen|aiohttp\.)\s*\([^;\n]{0,220}\b(?:req(?:uest)?\.(?:query|params|body|url|form|args)|r\.URL\.Query\(\)\.Get|request\.(?:args|form|json|query_params)|params\s*\[["']url["']|body\s*\[["']url["'])`, 5, 5, "content-risk"),
	pattern("ssrf_cloud_metadata_endpoint", `(?:https?://(?:169\.254\.169\.254|169\.254\.170\.2|100\.100\.100\.200|metadata\.google\.internal|\[?fd00:ec2::254\]?)[^"'\s]*|["'](?:169\.254\.169\.254|169\.254\.170\.2|100\.100\.100\.200|metadata\.google\.internal|fd00:ec2::254|IMDSv?2?)["'])`, 5, 5, "content-risk"),
	pattern("ssrf_non_http_scheme", `\b(?:file|gopher|dict|data|ftp|smb|smtp)://`, 4, 5, "content-risk"),
	pattern("unsafe_python_deserialization", `\b(?:pickle|cPickle|_pickle|dill|joblib)\.(?:load|loads)\s*\(`, 5, 5, "content-risk"),
	pattern("unsafe_yaml_load", `\byaml\.load\s*\((?:(?!SafeLoader|safe_load|FullLoader).){0,240}\)`, 4, 5, "content-risk"),
	pattern("php_unserialize_user_input", `(?is)\bunserialize\s*\(\s*(?:\$_(?:GET|POST|REQUEST|COOKIE)|filter_input\s*\(|base64_decode\s*\(\s*\$_(?:GET|POST|REQUEST|COOKIE))`, 5, 5, "content-risk"),
	pattern("java_native_deserialization", `\b(?:new\s+ObjectInputStream\s*\(|\.readObject\s*\(|\.readUnshared\s*\(|implements\s+Serializable\b)`, 4, 5, "content-risk"),
	pattern("java_xml_object_deserialization", `\b(?:new\s+XMLDecoder\s*\(|new\s+XStream\s*\(|\.fromXML\s*\()`, 4, 5, "content-risk"),
	pattern("dotnet_unsafe_deserialization", `\b(?:new\s+(?:BinaryFormatter|NetDataContractSerializer|LosFormatter|ObjectStateFormatter)\s*\(|(?:BinaryFormatter|NetDataContractSerializer|LosFormatter|ObjectStateFormatter)\s*\.\s*Deserialize\s*\()`, 5, 5, "content-risk"),
	pattern("xxe_java_xml_factory_surface", `\b(?:DocumentBuilderFactory|SAXParserFactory|XMLInputFactory|TransformerFactory|SchemaFactory)\.newInstance\s*\(`, 3, 5, "content-risk"),
	pattern("xxe_java_unsafe_feature_enabled", `(?is)\b(?:setFeature|setProperty)\s*\([^;\n]{0,220}(?:external-general-entities|external-parameter-entities|load-external-dtd|ACCESS_EXTERNAL_DTD|ACCESS_EXTERNAL_SCHEMA|SUPPORT_DTD|isSupportingExternalEntities)[^;\n]{0,80}\b(?:true|all|file|http)\b`, 5, 5, "content-risk"),
	pattern("xxe_python_lxml_unsafe_options", `(?is)\b(?:etree\.)?XMLParser\s*\([^)]*\b(?:resolve_entities\s*=\s*True|load_dtd\s*=\s*True|no_network\s*=\s*False|huge_tree\s*=\s*True)`, 5, 5, "content-risk"),
	pattern("xxe_php_libxml_entity_expansion", `(?is)\b(?:simplexml_load_string|simplexml_load_file|DOMDocument\s*::\s*loadXML|->\s*loadXML|->\s*load)\s*\([^;\n]{0,220}\b(?:LIBXML_NOENT|LIBXML_DTDLOAD|LIBXML_DTDATTR)\b`, 5, 5, "content-risk"),
	pattern("xxe_dotnet_dtd_or_resolver_enabled", `\b(?:DtdProcessing\s*=\s*DtdProcessing\.Parse|XmlResolver\s*=\s*new\s+XmlUrlResolver\s*\()`, 5, 5, "content-risk"),
	pattern("xxe_c_libxml_entity_expansion", `(?is)\b(?:xmlCtxtReadDoc|xmlCtxtReadFd|xmlCtxtReadFile|xmlCtxtReadIO|xmlCtxtReadMemory|xmlReadDoc|xmlReadFd|xmlReadFile|xmlReadIO|xmlReadMemory|xmlCtxtUseOptions)\s*\([^;\n]{0,260}\b(?:XML_PARSE_NOENT|XML_PARSE_DTDLOAD|XML_PARSE_DTDATTR)\b`, 5, 5, "content-risk"),
	pattern("prototype_pollution_key_literal", `["'](?:__proto__|constructor\.prototype|prototype\.constructor)["']`, 4, 5, "content-risk"),
	pattern("prototype_pollution_request_merge", `(?is)\b(?:Object\.assign|\$\.extend|jQuery\.extend|lodash\.merge|_\.merge|merge|deepmerge)\s*\([^;\n]{0,260}\b(?:req\.(?:query|body|params)|request\.(?:query|body|params)|ctx\.(?:query|request\.body)|event\.(?:queryStringParameters|body))\b`, 5, 5, "content-risk"),
	pattern("prototype_pollution_path_setter", `(?is)\b(?:set|setWith|objectPath\.set|dotProp\.set|lodash\.set|_\.set)\s*\([^;\n]{0,220}\b(?:req\.(?:query|body|params)|request\.(?:query|body|params)|ctx\.(?:query|request\.body)|event\.(?:queryStringParameters|body))\b`, 5, 5, "content-risk"),
	pattern("graphql_server_surface", `\b(?:new\s+ApolloServer\s*\(|graphqlHTTP\s*\(|createYoga\s*\(|GraphQLModule\.(?:forRoot|forRootAsync)\s*\(|GraphQLServer\s*\(|buildSchema\s*\(|makeExecutableSchema\s*\()`, 3, 5, "content-risk"),
	pattern("graphql_schema_tooling_enabled", `\b(?:graphiql|playground|introspection)\s*:\s*true\b`, 5, 5, "content-risk"),
	pattern("graphql_batching_enabled", `\b(?:allowBatchedHttpRequests|allowBatchedRequests|batching|batched)\s*:\s*true\b`, 4, 5, "content-risk"),
	pattern("graphql_node_id_access_field", `(?im)^\s*(?:node|nodes)\s*\([^\n)]*\b(?:id|ids)\s*:\s*\[?\s*ID\b`, 4, 5, "content-risk"),
	pattern("websocket_endpoint_surface", `\b(?:new\s+WebSocket\.Server\s*\(|new\s+Server\s*\([^\n;]{0,80}\bws\b|socket\.io|@app\.websocket\s*\(|websocket_endpoint\s*\(|websockets\.serve\s*\(|websocket\.Accept\s*\(|websocket\.Upgrader\s*\{)`, 3, 5, "content-risk"),
	pattern("websocket_insecure_ws_url", `\bw[s]://(?!localhost\b|127\.0\.0\.1\b|\[?::1\]?)`, 5, 5, "content-risk"),
	pattern("websocket_allow_all_origin", `(?is)\b(?:CheckOrigin\s*:\s*func|verifyClient\s*:\s*\(?\s*(?:async\s*)?function|verifyClient\s*:\s*\([^\)]*\)\s*=>|verifyClient\s*:\s*\w+\s*=>)[^;\n]{0,360}\breturn\s+true\b|\bcors\s*:\s*\{[^}\n]{0,180}\borigin\s*:\s*["']\*["']`, 5, 5, "content-risk"),
	pattern("websocket_compression_enabled", `\b(?:perMessageDeflate|permessage-deflate)\s*:\s*true\b`, 4, 5, "content-risk"),
	pattern("file_upload_boundary", `\b(multipart|chunked\s+upload|resumable\s+upload|upload\s+stream|file\s+upload)\b`, 3, 5, "content-risk"),
	pattern("path_traversal_user_file_access", `(?is)\b(?:fs\.(?:readFile|readFileSync|createReadStream)|os\.(?:Open|ReadFile)|open|FileResponse|http\.ServeFile|sendFile|download)\s*\([^;\n]{0,240}\b(?:req(?:uest)?\.(?:query|params|body|url|form|args)|r\.URL\.Query\(\)\.Get|request\.(?:args|form|json|query_params|nextUrl\.searchParams)|params\s*\[["'](?:path|file|filename)["']|searchParams\.get\s*\(\s*["'](?:path|file|filename)["'])`, 5, 5, "content-risk"),
	pattern("path_join_request_input", `(?is)\b(?:path\.(?:join|resolve)|filepath\.(?:Join|Clean|Abs)|os\.path\.(?:join|abspath|normpath))\s*\([^;\n]{0,240}\b(?:req(?:uest)?\.(?:query|params|body|url|form|args)|r\.URL\.Query\(\)\.Get|request\.(?:args|form|json|query_params|nextUrl\.searchParams)|params\s*\[["'](?:path|file|filename)["']|searchParams\.get\s*\(\s*["'](?:path|file|filename)["'])`, 5, 5, "content-risk"),
	pattern("upload_user_filename_write", `(?is)\b(?:fs\.(?:writeFile|writeFileSync|createWriteStream)|os\.(?:Create|OpenFile)|open|createWriteStream|saveAs|mv|move)\s*\([^;\n]{0,260}\b(?:file\.originalname|req\.file\.originalname|req\.files?\[[^\]]+\]\.name|uploadedFile\.name|UploadFile\.filename|\.Filename\b)`, 5, 5, "content-risk"),
	pattern("archive_extract_surface", `\b(?:extractall\s*\(|tarfile\.open\s*\(|zipfile\.ZipFile\s*\(|new\s+AdmZip\s*\(|extract[-]zip|unzipper\.|archiver\.|archive[/]zip|tar\.x\s*\(|tar\.extract\s*\()`, 4, 5, "content-risk"),
	pattern("hardcoded_private_key", `-----BEGIN [A-Z ]*PRIVATE KEY-----`, 5, 3, "secret-risk"),
	pattern("provider_token_literal", `\b(?:sk-[A-Za-z0-9_-]{20,}|ghp_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{20,})`, 5, 5, "secret-risk"),
	pattern("credential_assignment_literal", `(?i)\b(password|passwd|pwd|api[_-]?key|token|secret)\b\s*[:=]\s*["'][^"']{12,}["']`, 4, 5, "secret-risk"),
	pattern("background_context", `\bcontext\.Background\s*\(`, 2, 4, "unknowns"),
	pattern("resource_lifecycle", `\b(Open|Connect|NewClient|NewRequest|http\.Client|sql\.Open)\s*\(`, 2, 4, "unknowns"),
	pattern("read_all_or_global_growth", `\bio\.ReadAll\s*\(|append\s*\([^)]*\.\.\.\)`, 3, 5, "unknowns"),
	pattern("go_module_replace", `(?m)^replace\s+`, 3, 5, "dependency-health"),
	pattern("os_specific_command", `\b(?:open|osascript|xdg-open|cmd\.exe|powershell)\b`, 3, 5, "content-risk"),
	pattern("error_context_dropped", `(?m)return\s+err\s*$`, 3, 5, "content-risk"),
}

func loadScoringPatterns(path string) (scoringPatterns, error) {
	if path == "" {
		patterns, err := parseScoringPatterns(embeddedTriagePatterns, "embedded:triage_patterns.json")
		if err != nil {
			return scoringPatterns{}, err
		}
		return patterns, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return scoringPatterns{}, fmt.Errorf("load patterns: %w", err)
	}
	patterns, err := parseScoringPatterns(data, path)
	if err != nil {
		return scoringPatterns{}, err
	}
	if abs, err := filepath.Abs(path); err == nil {
		patterns.Source = abs
	}
	return patterns, nil
}

func parseScoringPatterns(data []byte, source string) (scoringPatterns, error) {
	var raw struct {
		PathTerms []struct {
			Term   string `json:"term"`
			Weight int    `json:"weight"`
		} `json:"path_terms"`
		ContentPatterns []struct {
			ID         string `json:"id"`
			Pattern    string `json:"pattern"`
			Weight     int    `json:"weight"`
			MaxMatches int    `json:"max_matches"`
		} `json:"content_patterns"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return scoringPatterns{}, fmt.Errorf("parse patterns: %w", err)
	}
	patterns := scoringPatterns{Source: source}
	for index, item := range raw.PathTerms {
		term := strings.TrimSpace(item.Term)
		if term == "" {
			return scoringPatterns{}, fmt.Errorf("path_terms[%d].term is required", index)
		}
		if item.Weight <= 0 {
			return scoringPatterns{}, fmt.Errorf("path_terms[%d].weight must be positive", index)
		}
		patterns.PathTerms = append(patterns.PathTerms, fallbackTerm{Term: term, Weight: item.Weight, Layer: "path-risk"})
	}
	for index, item := range raw.ContentPatterns {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = "pattern_" + itoa(index)
		}
		if strings.TrimSpace(item.Pattern) == "" {
			return scoringPatterns{}, fmt.Errorf("content_patterns[%s].pattern is required", id)
		}
		if item.Weight <= 0 {
			return scoringPatterns{}, fmt.Errorf("content_patterns[%s].weight must be positive", id)
		}
		if item.MaxMatches <= 0 {
			return scoringPatterns{}, fmt.Errorf("content_patterns[%s].max_matches must be positive", id)
		}
		compiled, err := regexp2.Compile(item.Pattern, regexp2.None)
		if err != nil {
			return scoringPatterns{}, fmt.Errorf("content_patterns[%s].pattern is invalid: %w", id, err)
		}
		compiled.MatchTimeout = 100 * time.Millisecond
		patterns.ContentPatterns = append(patterns.ContentPatterns, contentPattern{
			ID:         id,
			Pattern:    compiled,
			Weight:     item.Weight,
			MaxMatches: item.MaxMatches,
			Layer:      "content-risk",
		})
	}
	if len(patterns.PathTerms) == 0 && len(patterns.ContentPatterns) == 0 {
		return scoringPatterns{}, fmt.Errorf("patterns file must define path_terms or content_patterns: %s", source)
	}
	return patterns, nil
}

func pattern(id, expr string, weight, maxMatches int, layer string) contentPattern {
	compiled := regexp2.MustCompile(expr, regexp2.None)
	compiled.MatchTimeout = 100 * time.Millisecond
	return contentPattern{
		ID:         id,
		Pattern:    compiled,
		Weight:     weight,
		MaxMatches: maxMatches,
		Layer:      layer,
	}
}

func pathRisk(patterns scoringPatterns, rel string) (int, []string) {
	lower := strings.ToLower(rel)
	score := 0
	var reasons []string
	for _, term := range patterns.PathTerms {
		if pathTermMatches(lower, term.Term) {
			score += term.Weight
			reasons = append(reasons, "path:"+term.Term)
		}
	}
	return score, reasons
}

func pathTermMatches(path, term string) bool {
	if strings.Contains(term, ".") {
		return strings.Contains(path, term)
	}
	re := regexp.MustCompile(`(^|[^a-z0-9])` + regexp.QuoteMeta(term) + `([^a-z0-9]|$)`)
	return re.FindStringIndex(path) != nil
}

func contentRisk(patterns scoringPatterns, rel, text string) (int, []string) {
	if contentPatternSkipped(rel) {
		return 0, nil
	}
	text = textWithoutDetectorLiterals(text)
	score := 0
	var reasons []string
	for _, item := range patterns.ContentPatterns {
		matches, err := countRegexp2Matches(item.Pattern, text, item.MaxMatches)
		if err != nil || matches == 0 {
			continue
		}
		score += matches * item.Weight
		reasons = append(reasons, "content:"+item.ID+":"+itoa(matches))
	}
	return score, reasons
}

func countRegexp2Matches(pattern *regexp2.Regexp, text string, maxMatches int) (int, error) {
	match, err := pattern.FindStringMatch(text)
	if err != nil {
		return 0, err
	}
	count := 0
	for match != nil && count < maxMatches {
		count++
		match, err = pattern.FindNextMatch(match)
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

func contentPatternSkipped(rel string) bool {
	switch strings.ToLower(filepathExt(rel)) {
	case ".json", ".md", ".toml", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func architectureSmellRisk(text, rel string, lines int) (int, int, []string) {
	if !isArchitectureSource(rel) {
		return 0, 0, nil
	}
	imports := len(regexp.MustCompile(`(?m)^\s*(import\s|from\s+\S+\s+import\s|use\s+)`).FindAllStringIndex(text, -1))
	methodPrefixes := map[string]bool{}
	for _, match := range regexp.MustCompile(`\b(validate|format|send|query|fetch|build|parse|render|handle)[A-Z_]`).FindAllStringSubmatch(text, -1) {
		methodPrefixes[strings.ToLower(match[1])] = true
	}
	caseCount := len(regexp.MustCompile(`(?m)^\s*(case\s+|default\s*:)`).FindAllStringIndex(text, -1))
	score := 0
	var reasons []string
	if lines >= 500 && imports >= 15 {
		score += 5
		reasons = append(reasons, "smell:large_import_fanout:"+itoa(imports))
	} else if imports >= 25 {
		score += 4
		reasons = append(reasons, "smell:import_fanout:"+itoa(imports))
	}
	if lines >= 500 && len(methodPrefixes) >= 4 {
		score += 4
		reasons = append(reasons, "smell:mixed_method_prefixes:"+itoa(len(methodPrefixes)))
	}
	if caseCount >= 8 {
		score += 4
		reasons = append(reasons, "smell:case_cascade:"+itoa(caseCount))
	}
	return imports, score, reasons
}

func unknownsRisk(rel, text string) (int, []string) {
	if !isArchitectureSource(rel) {
		return 0, nil
	}
	score := 0
	var reasons []string
	if count := len(regexp.MustCompile(`\bos\.(Getenv|LookupEnv)\s*\(`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "unknowns:env_assumptions:"+itoa(count))
	}
	if count := len(regexp.MustCompile(`http://(?:127\.0\.0\.1|localhost|\[::1\])|:\d{4,5}/`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "unknowns:hardcoded_runtime_endpoint:"+itoa(count))
	}
	if regexp.MustCompile(`(?s)\bfor\b.*\bfor\b`).FindStringIndex(text) != nil {
		score += 2
		reasons = append(reasons, "unknowns:nested_loop_scale:1")
	}
	if count := len(regexp.MustCompile(`\b(NewClient|sql\.Open|http\.Client|regexp\.MustCompile)\s*\(`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "unknowns:resource_factory:"+itoa(count))
	}
	if count := len(regexp.MustCompile(`(?i)\b(utils?|helpers?|common)\b`).FindAllStringIndex(rel, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "unknowns:custom_infra:"+itoa(count))
	}
	return score, reasons
}

func envVarsInCode(rel, text string) []string {
	if !isArchitectureSource(rel) {
		return nil
	}
	seen := map[string]bool{}
	for _, match := range regexp.MustCompile(`\bos\.(?:Getenv|LookupEnv)\(\s*["']([A-Z][A-Z0-9_]{1,})["']`).FindAllStringSubmatch(text, -1) {
		seen[match[1]] = true
	}
	var out []string
	for env := range seen {
		out = append(out, env)
	}
	sortStrings(out)
	return out
}

func envContractRisk(envVars []string, documented map[string]bool, churn, fixTouches, pathScore, contentScore, unknownsScore int) (int, []string) {
	var missing []string
	for _, env := range envVars {
		if !documented[env] {
			missing = append(missing, env)
		}
	}
	if len(missing) == 0 {
		return 0, nil
	}
	riskOverlap := pathScore >= 2 || contentScore >= 4 || unknownsScore >= 4
	changePressure := churn >= 120 || fixTouches > 0
	if len(missing) == 1 && !riskOverlap && !changePressure {
		return 0, nil
	}
	score := min(len(missing)*3, 6)
	reasons := []string{"env_contract:undocumented:" + itoa(len(missing)), "env_contract:vars:" + strings.Join(missing, ",")}
	if riskOverlap {
		score++
		reasons = append(reasons, "env_contract:risk_overlap")
	}
	if changePressure {
		score++
		reasons = append(reasons, "env_contract:change_pressure")
	}
	return score, reasons
}

func centralityRisk(incomingRefs, pathScore, contentScore int) (int, []string) {
	score := 0
	var reasons []string
	switch {
	case incomingRefs >= 10:
		score += 5
	case incomingRefs >= 5:
		score += 4
	case incomingRefs >= 2:
		score += 3
	}
	if score == 0 {
		return 0, nil
	}
	reasons = append(reasons, "centrality:incoming_refs:"+itoa(incomingRefs))
	if pathScore >= 3 || contentScore >= 6 {
		score++
		reasons = append(reasons, "centrality:risk_overlap")
	}
	return score, reasons
}

func cochangeRisk(info cochangeInfo, churn, fixTouches, pathScore, contentScore, centralityScore int) (int, []string) {
	if info.PartnerCount == 0 {
		return 0, nil
	}
	riskContext := fixTouches > 0 || churn >= 120 || pathScore >= 3 || contentScore >= 6 || centralityScore > 0
	if !riskContext {
		return 0, nil
	}
	score := 0
	var reasons []string
	if info.PartnerCount >= 4 {
		score += 5
	} else if info.PartnerCount >= 2 {
		score += 4
	} else {
		score += 2
	}
	reasons = append(reasons, "cochange:partners:"+itoa(info.PartnerCount))
	if info.MaxJaccard >= 0.5 {
		score += 2
	} else if info.MaxJaccard >= 0.3 {
		score++
	}
	reasons = append(reasons, "cochange:max_jaccard:"+formatFloat(info.MaxJaccard))
	if fixTouches > 0 {
		score += 2
		reasons = append(reasons, "cochange:bugfix_overlap")
	}
	if churn >= 120 {
		score++
		reasons = append(reasons, "cochange:churn_overlap")
	}
	return score, reasons
}

func ownershipRisk(info ownershipInfo, churn, fixTouches, pathScore, contentScore int) (int, []string) {
	if info.Touches == 0 {
		return 0, nil
	}
	riskContext := pathScore >= 2 || contentScore >= 4 || fixTouches > 0 || churn >= 120
	if !riskContext {
		return 0, nil
	}
	score := 0
	var reasons []string
	if info.AuthorCount == 1 {
		score += 4
		reasons = append(reasons, "ownership:risky_single_author")
	} else if info.AuthorCount == 2 {
		score += 2
		reasons = append(reasons, "ownership:risky_two_authors")
	}
	if info.TopShare >= 0.8 {
		score += 3
		reasons = append(reasons, "ownership:top_author_share:"+formatFloat(info.TopShare))
	} else if info.TopShare >= 0.65 {
		score += 2
		reasons = append(reasons, "ownership:top_author_share:"+formatFloat(info.TopShare))
	}
	if info.Touches >= 5 {
		score += 2
		reasons = append(reasons, "ownership:concentrated_touches:"+itoa(info.Touches))
	}
	return score, reasons
}

func staleMarkerRisk(info staleMarkerInfo, churn, fixTouches, pathScore, contentScore, ownershipScore int) (int, []string) {
	if info.StaleCount == 0 {
		return 0, nil
	}
	riskContext := fixTouches > 0 || churn >= 120 || pathScore >= 3 || contentScore >= 6 || ownershipScore > 0
	if !riskContext {
		return 0, nil
	}
	score := min(info.StaleCount*2, 6)
	reasons := []string{"stale_marker:count:" + itoa(info.StaleCount), "stale_marker:oldest_days:" + itoa(info.OldestDays)}
	if info.OldestDays >= 365 {
		score += 2
		reasons = append(reasons, "stale_marker:older_than_year")
	} else if info.OldestDays >= 180 {
		score++
		reasons = append(reasons, "stale_marker:older_than_180d")
	}
	if fixTouches > 0 {
		score++
		reasons = append(reasons, "stale_marker:bugfix_overlap")
	}
	return score, reasons
}

func dependencyHealthRisk(rel, text string) (int, []string) {
	name := strings.ToLower(filepathBase(rel))
	score := 0
	var reasons []string
	switch name {
	case "go.mod":
		if strings.Contains(text, "\nreplace ") || strings.HasPrefix(text, "replace ") {
			score += 4
			reasons = append(reasons, "dependency_health:go_module_replace")
		}
	case "package.json":
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return 2, []string{"dependency_health:unparseable_package_json"}
		}
		directCount := jsonObjectLen(parsed, "dependencies") + jsonObjectLen(parsed, "devDependencies") + jsonObjectLen(parsed, "optionalDependencies")
		if directCount > 20 {
			score += 4
			reasons = append(reasons, "dependency_health:large_dependency_tree:"+itoa(directCount))
		}
		peerCount := jsonObjectLen(parsed, "peerDependencies")
		if peerCount > 0 {
			score += min(peerCount*2, 6)
			reasons = append(reasons, "dependency_health:peer_dependency_surface:"+itoa(peerCount))
		}
		if _, ok := parsed["license"]; !ok {
			score += 2
			reasons = append(reasons, "dependency_health:missing_license")
		}
	case "composer.json":
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return 2, []string{"dependency_health:unparseable_composer_json"}
		}
		directCount := jsonObjectLen(parsed, "require") + jsonObjectLen(parsed, "require-dev")
		if directCount > 20 {
			score += 4
			reasons = append(reasons, "dependency_health:large_dependency_tree:"+itoa(directCount))
		}
		conflictCount := jsonObjectLen(parsed, "conflict")
		if conflictCount > 0 {
			score += min(conflictCount*2, 6)
			reasons = append(reasons, "dependency_health:conflict_constraints:"+itoa(conflictCount))
		}
		if _, ok := parsed["license"]; !ok {
			score += 2
			reasons = append(reasons, "dependency_health:missing_license")
		}
	case "pyproject.toml":
		directCount := countPyprojectDependencies(text)
		if directCount > 20 {
			score += 4
			reasons = append(reasons, "dependency_health:large_dependency_tree:"+itoa(directCount))
		}
		if strings.Contains(text, "[project]") && !regexpMust(`(?m)^license\s*=`).MatchString(text) {
			score += 2
			reasons = append(reasons, "dependency_health:missing_license")
		}
	case "requirements.txt", "requirements.in":
		directCount := countRequirementLines(text)
		if directCount > 20 {
			score += 4
			reasons = append(reasons, "dependency_health:large_dependency_tree:"+itoa(directCount))
		}
	}
	return score, reasons
}

func jsonObjectLen(parsed map[string]any, key string) int {
	items, ok := parsed[key].(map[string]any)
	if !ok {
		return 0
	}
	return len(items)
}

func countRequirementLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
			continue
		}
		count++
	}
	return count
}

func countPyprojectDependencies(text string) int {
	count := 0
	inDependencyArray := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "dependencies") || strings.Contains(trimmed, "dependencies = [") {
			inDependencyArray = true
		}
		if inDependencyArray && regexpMust(`^["'][A-Za-z0-9_.-]+`).MatchString(trimmed) {
			count++
		}
		if inDependencyArray && strings.Contains(trimmed, "]") {
			inDependencyArray = false
		}
	}
	return count
}

func testFlakeRisk(rel, text string) (int, []string) {
	if !isTestFile(rel) {
		return 0, nil
	}
	score := 0
	var reasons []string
	if count := len(regexp.MustCompile(`\btime\.Sleep\s*\(`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*3, 6)
		reasons = append(reasons, "flake:fixed_wait:"+itoa(count))
	}
	if count := len(regexp.MustCompile(`\b(rand\.|time\.Now\s*\(|httptest\.|http\.Get\s*\()`).FindAllStringIndex(text, -1)); count > 0 {
		score += min(count*2, 6)
		reasons = append(reasons, "flake:nondeterministic_or_io:"+itoa(count))
	}
	return score, reasons
}

func testOracleRisk(rel, text string, lines int) (int, []string) {
	if !isTestFile(rel) {
		return 0, nil
	}
	testCases := len(regexp.MustCompile(`\bfunc\s+Test\w+|\.Run\s*\(`).FindAllStringIndex(text, -1))
	assertions := len(regexp.MustCompile(`\b(t\.Fatal|t\.Error|t\.Fail|if\s+.*!=|if\s+.*==|assert\.|require\.)`).FindAllStringIndex(text, -1))
	if testCases >= 3 && assertions == 0 {
		return 6, []string{"oracle:no_assertions"}
	}
	if lines >= 200 && assertions < 3 {
		return 4, []string{"oracle:large_weak_test"}
	}
	return 0, nil
}

func branchComplexity(text, rel string) int {
	if !isArchitectureSource(rel) {
		return 0
	}
	return len(regexp.MustCompile(`\b(if|else\s+if|for|while|case|catch|except|switch|select|match)\b|&&|\|\||\?`).FindAllStringIndex(text, -1))
}

func hotspotRisk(complexity, churn, fixTouches, incomingRefs int) (int, []string) {
	score := 0
	var reasons []string
	if complexity >= 40 && (fixTouches > 0 || churn >= 120 || incomingRefs >= 5) {
		score += 4
		reasons = append(reasons, "hotspot:complexity_with_pressure:"+itoa(complexity))
	}
	if complexity >= 25 && fixTouches > 0 {
		score += 2
		reasons = append(reasons, "hotspot:bugfix_complexity")
	}
	return score, reasons
}

func seedScore(row FileEvidence) float64 {
	sizeComponent := math.Min(float64(row.Lines)/300.0, 5)
	churnComponent := math.Min(float64(row.Churn)/120.0, 5)
	fixComponent := math.Min(float64(row.FixTouches), 5)
	markerComponent := math.Min(float64(row.Markers), 5)
	riskComponent := math.Min(float64(row.PathRisk)/3.0, 5)
	contentComponent := math.Min(float64(row.ContentRisk)/3.0, 5)
	smellComponent := math.Min(float64(row.SmellRisk)/3.0, 5)
	hotspotComponent := math.Min(float64(row.HotspotRisk)/3.0, 5)
	sdkComponent := math.Min(float64(row.SDKDXRisk)/3.0, 5)
	unknownsComponent := math.Min(float64(row.UnknownsRisk)/3.0, 5)
	envComponent := math.Min(float64(row.EnvContractRisk)/3.0, 5)
	workflowComponent := math.Min(float64(row.WorkflowSecurityRisk)/3.0, 5)
	migrationComponent := math.Min(float64(row.MigrationSafetyRisk)/3.0, 5)
	containerComponent := math.Min(float64(row.ContainerBuildRisk)/3.0, 5)
	kubernetesComponent := math.Min(float64(row.KubernetesSecurityRisk)/3.0, 5)
	terraformComponent := math.Min(float64(row.TerraformSecurityRisk)/3.0, 5)
	openAPIComponent := math.Min(float64(row.OpenAPIContractRisk)/3.0, 5)
	corsComponent := math.Min(float64(row.CORSSecurityRisk)/3.0, 5)
	cookieComponent := math.Min(float64(row.CookieSecurityRisk)/3.0, 5)
	dependencyComponent := math.Min(float64(row.DependencyHealthRisk)/3.0, 5)
	centralityComponent := math.Min(float64(row.CentralityRisk)/3.0, 5)
	cochangeComponent := math.Min(float64(row.CochangeRisk)/3.0, 5)
	ownershipComponent := math.Min(float64(row.OwnershipRisk)/3.0, 5)
	flakeComponent := math.Min(float64(row.FlakeRisk)/3.0, 5)
	oracleComponent := math.Min(float64(row.OracleRisk)/3.0, 5)
	staleMarkerComponent := math.Min(float64(row.StaleMarkerRisk)/3.0, 5)
	layerComponent := math.Min(float64(len(row.EvidenceLayers)), 5)
	total := 0.18*churnComponent +
		0.15*fixComponent +
		0.18*riskComponent +
		0.18*contentComponent +
		0.08*smellComponent +
		0.05*hotspotComponent +
		0.04*sdkComponent +
		0.02*unknownsComponent +
		0.04*envComponent +
		0.05*workflowComponent +
		0.05*migrationComponent +
		0.04*containerComponent +
		0.05*kubernetesComponent +
		0.05*terraformComponent +
		0.04*openAPIComponent +
		0.04*corsComponent +
		0.04*cookieComponent +
		0.03*dependencyComponent +
		0.05*centralityComponent +
		0.05*cochangeComponent +
		0.04*ownershipComponent +
		0.04*flakeComponent +
		0.04*oracleComponent +
		0.04*staleMarkerComponent +
		0.10*sizeComponent +
		0.05*markerComponent +
		0.02*layerComponent
	return math.Round(total*100) / 100
}

func qualitativeScore(row FileEvidence) int {
	maxComponent := 0
	for _, value := range []int{
		row.PathRisk,
		row.ContentRisk,
		row.SmellRisk,
		row.HotspotRisk,
		row.SDKDXRisk,
		row.UnknownsRisk,
		row.EnvContractRisk,
		row.WorkflowSecurityRisk,
		row.MigrationSafetyRisk,
		row.ContainerBuildRisk,
		row.KubernetesSecurityRisk,
		row.TerraformSecurityRisk,
		row.OpenAPIContractRisk,
		row.CORSSecurityRisk,
		row.CookieSecurityRisk,
		row.DependencyHealthRisk,
		row.CentralityRisk,
		row.CochangeRisk,
		row.OwnershipRisk,
		row.FlakeRisk,
		row.OracleRisk,
		row.StaleMarkerRisk,
	} {
		if value > maxComponent {
			maxComponent = value
		}
	}
	score := int(math.Ceil(float64(maxComponent) / 3.0))
	if len(row.EvidenceLayers) >= 2 {
		score++
	}
	if score < 1 {
		return 1
	}
	if score > 5 {
		return 5
	}
	return score
}
