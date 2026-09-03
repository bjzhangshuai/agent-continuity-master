#!/bin/sh
set -eu

PATH="${RANCHER_DESKTOP_BIN:-$HOME/.rd/bin}:$PATH"
export PATH

context=rancher-desktop
namespace=${CONTINUITY_NAMESPACE:-agent-continuity-dev}
release=${CONTINUITY_RELEASE:-agent-continuity}
api_url=${AGENTCLOUD_API_URL:-http://127.0.0.1:18080}
repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cli="$repository_dir/bin/agentcloud"
seed_id=agent-continuity-operations-v1
port_forward_pid=
temporary_dir=$(mktemp -d)

cleanup() {
  if [ -n "$port_forward_pid" ]; then
    kill "$port_forward_pid" 2>/dev/null || true
    wait "$port_forward_pid" 2>/dev/null || true
  fi
  rm -rf "$temporary_dir"
}
trap cleanup EXIT INT TERM

require_command() { command -v "$1" >/dev/null 2>&1 || { echo "required command not found: $1" >&2; exit 1; }; }
require_command kubectl
require_command curl
require_command jq
require_command go
require_command openssl

echo "Building the current agentcloud CLI..."
mkdir -p "$repository_dir/bin"
(cd "$repository_dir" && GOMAXPROCS=4 go build -trimpath -o bin/agentcloud ./cmd/agentcloud)

server=$(kubectl config view --context "$context" --minify -o jsonpath='{.clusters[0].cluster.server}')
case "$server" in
  https://127.0.0.1:*|https://localhost:*) ;;
  *) echo "refusing demo seed: $context is not a loopback cluster" >&2; exit 1 ;;
esac
node_names=$(kubectl --context "$context" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
case "$node_names" in
  *rancher-desktop*) ;;
  *) echo "refusing demo seed: node identity is not Rancher Desktop" >&2; exit 1 ;;
esac

if ! curl -fsS "$api_url/v1/health/ready" >/dev/null 2>&1; then
  kubectl --context "$context" -n "$namespace" port-forward "svc/$release" 18080:8080 >"$temporary_dir/port-forward.log" 2>&1 &
  port_forward_pid=$!
  attempts=0
  until curl -fsS "$api_url/v1/health/ready" >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 30 ]; then
      echo "local API did not become ready" >&2
      exit 1
    fi
    sleep 1
  done
fi

bootstrap_token=${AGENTCLOUD_BOOTSTRAP_TOKEN:-}
if [ -z "$bootstrap_token" ]; then
  bootstrap_token=$(kubectl --context "$context" -n "$namespace" get secret agent-continuity-secrets -o jsonpath='{.data.bootstrap-token}' | base64 -d)
fi
export AGENTCLOUD_BOOTSTRAP_TOKEN="$bootstrap_token"
export AGENTCLOUD_API_URL="$api_url"

if [ -z "${AGENTCLOUD_STATE_DIR:-}" ]; then
  case "$(uname -s)" in
    Darwin) AGENTCLOUD_STATE_DIR="$HOME/Library/Application Support/agent-continuity" ;;
    *) AGENTCLOUD_STATE_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/agent-continuity" ;;
  esac
fi
export AGENTCLOUD_STATE_DIR
mkdir -p "$AGENTCLOUD_STATE_DIR"
chmod 700 "$AGENTCLOUD_STATE_DIR"

credentials_file="$AGENTCLOUD_STATE_DIR/credentials.json"
device_file="$AGENTCLOUD_STATE_DIR/device.json"
if [ -f "$credentials_file" ]; then
  account_id=$(jq -r '.account_id' "$credentials_file")
else
  account_id=$(curl -fsS -X POST \
    -H "X-Bootstrap-Token: $bootstrap_token" \
    -H 'Content-Type: application/json' \
    -d '{"name":"Agent Continuity 本地运营演示"}' \
    "$api_url/v1/accounts" | jq -r '.account_id')
fi

"$cli" login "$account_id" local-demo >/dev/null
access_token=$(jq -r '.access_token' "$credentials_file")

api() {
  method=$1
  path=$2
  body=${3:-}
  response_file="$temporary_dir/api-response"
  header_file="$temporary_dir/api-headers"
  api_attempt=0
  while :; do
    api_attempt=$((api_attempt + 1))
    if [ -n "$body" ]; then
      status=$(curl -sS -D "$header_file" -o "$response_file" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $access_token" -H 'Content-Type: application/json' -d "$body" "$api_url$path") || return
    else
      status=$(curl -sS -D "$header_file" -o "$response_file" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $access_token" "$api_url$path") || return
    fi
    case "$status" in
      2??)
        cat "$response_file"
        return 0
        ;;
      429)
        if [ "$api_attempt" -ge 5 ]; then
          cat "$response_file" >&2
          return 22
        fi
        retry_after=$(awk 'tolower($1) == "retry-after:" {gsub("\\r", "", $2); print $2}' "$header_file" | tail -n 1)
        case "$retry_after" in
          ''|*[!0-9]*) retry_after=5 ;;
        esac
        if [ "$retry_after" -gt 59 ]; then retry_after=59; fi
        echo "Local API rate window reached; continuing in ${retry_after}s..." >&2
        sleep "$retry_after"
        ;;
      500|502|503|504)
        if [ "$api_attempt" -ge 5 ]; then
          cat "$response_file" >&2
          return 22
        fi
        sleep "$api_attempt"
        ;;
      *)
        cat "$response_file" >&2
        return 22
        ;;
    esac
  done
}

if [ ! -f "$device_file" ]; then
  "$cli" device init demo-operations-mac >/dev/null
fi
device_id=$(jq -r '.device_id' "$device_file")

organization_id=$(api GET /v1/organizations | jq -r --arg seed "$seed_id" 'first(.organizations[] | select(.settings.demo_seed == $seed) | .organization_id) // ""')
if [ -z "$organization_id" ]; then
  organization_id=$(api POST /v1/organizations "$(jq -nc --arg seed "$seed_id" '{name:"Agent Continuity 演示组织",slug:"agent-continuity-demo",plan:"team",key_provider:"platform",settings:{demo_seed:$seed}}')" | jq -r '.organization_id')
fi

ensure_demo_device() {
  name=$1
  device=$(api GET '/v1/devices?include_revoked=true' | jq -r --arg name "$name" 'first(.devices[] | select(.name == $name and .revoked_at == null) | .device_id) // ""')
  if [ -z "$device" ]; then
    signing_key=$(openssl rand 32 | openssl base64 -A | tr '+/' '-_' | tr -d '=')
    encryption_key=$(openssl rand 32 | openssl base64 -A | tr '+/' '-_' | tr -d '=')
    device=$(api POST /v1/devices "$(jq -nc --arg name "$name" --arg public "$signing_key" --arg encryption "$encryption_key" '{name:$name,public_key:$public,encryption_public_key:$encryption}')" | jq -r '.device_id')
  fi
  printf %s "$device"
}

ensure_team() {
  key=$1
  name=$2
  slug=$3
  department=$4
  team=$(api GET "/v1/organizations/$organization_id/teams" | jq -r --arg key "$key" --arg seed "$seed_id" 'first(.teams[] | select(.settings.demo_seed == $seed and .settings.demo_key == $key) | .team_id) // ""')
  if [ -z "$team" ]; then
    team=$(api POST "/v1/organizations/$organization_id/teams" "$(jq -nc --arg key "$key" --arg seed "$seed_id" --arg name "$name" --arg slug "$slug" --arg department "$department" '{name:$name,slug:$slug,settings:{demo_seed:$seed,demo_key:$key,department:$department}}')" | jq -r '.team_id')
  fi
  printf %s "$team"
}

ensure_agent() {
  provider=$1
  key=$2
  name=$3
  model=$4
  capabilities=$5
  role=$6
  region=$7
  utilization=$8
  success_rate=$9
  agent_id=$(api GET /v1/agents | jq -r --arg provider "$provider" --arg key "$key" --arg seed "$seed_id" 'first(.agents[] | select(.provider == $provider and .card.demo_seed == $seed and ((.card.demo_key // "primary") == $key)) | .agent_id) // ""')
  if [ -z "$agent_id" ]; then
    body=$(jq -nc --arg name "$name" --arg provider "$provider" --arg model "$model" --arg seed "$seed_id" --arg key "$key" --arg role "$role" --arg region "$region" --argjson utilization "$utilization" --argjson success "$success_rate" --argjson capabilities "$capabilities" '{name:$name,provider:$provider,model:$model,capabilities:$capabilities,card:{demo_seed:$seed,demo_key:$key,managed:true,environment:"local-k8s",status:"online",role:$role,region:$region,utilization:$utilization,success_rate:$success,last_seen_minutes:2}}')
    agent_id=$(api POST /v1/agents "$body" | jq -r '.agent_id')
  fi
  printf %s "$agent_id"
}

ensure_api_key() {
  name=$1
  scopes=$2
  if ! api GET /v1/api-keys | jq -e --arg name "$name" '.api_keys[] | select(.name == $name and .revoked_at == null)' >/dev/null; then
    api POST /v1/api-keys "$(jq -nc --arg name "$name" --argjson scopes "$scopes" '{name:$name,scopes:$scopes,ttl_seconds:2592000}')" >/dev/null
  fi
}

ensure_oauth_client() {
  name=$1
  scopes=$2
  if ! api GET /v1/oauth/clients | jq -e --arg name "$name" '.clients[] | select(.name == $name and .revoked_at == null)' >/dev/null; then
    api POST /v1/oauth/clients "$(jq -nc --arg name "$name" --argjson scopes "$scopes" '{name:$name,scopes:$scopes,redirect_uris:["http://127.0.0.1:18080/demo/oauth/callback"]}')" >/dev/null
  fi
}

device_release=$(ensure_demo_device '发布流水线 · Linux Runner')
device_product=$(ensure_demo_device '产品负责人 · iPhone')
device_field=$(ensure_demo_device '现场支持 · Android')
device_support=$(ensure_demo_device '客户成功 · Windows')
device_quality=$(ensure_demo_device '质量中心 · Browser SDK')
device_security=$(ensure_demo_device '安全审计 · macOS')
: "$device_release" "$device_product" "$device_field" "$device_support" "$device_quality" "$device_security"

team_engineering=$(ensure_team platform-engineering '平台工程组' platform-engineering '研发')
team_customer=$(ensure_team customer-success '客户成功组' customer-success '客户成功')
team_quality=$(ensure_team quality-assurance '质量保障组' quality-assurance '质量')
team_data=$(ensure_team data-intelligence '数据智能组' data-intelligence '数据')
team_security=$(ensure_team security-compliance '安全合规组' security-compliance '安全')
team_product=$(ensure_team product-experience '产品体验组' product-experience '产品')

common_capabilities='["task_discovery","event_capture","rehydrate","artifacts"]'
codex_capabilities='["task_discovery","event_capture","native_resume","rehydrate","artifacts"]'
agent_codex=$(ensure_agent codex primary 'Codex 发布执行器' 'gpt-5.6' "$codex_capabilities" '实现与发布' cn-east 76 99)
agent_codex_review=$(ensure_agent codex review 'Codex 自动修复执行器' 'gpt-5.6' "$codex_capabilities" '故障自动修复' cn-north 61 98)
agent_codex_backup=$(ensure_agent codex backup 'Codex 灾备恢复执行器' 'gpt-5.4' "$codex_capabilities" '灾备接管' cn-south 34 97)
agent_claude=$(ensure_agent claude primary 'Claude 代码审查员' 'claude-code' "$common_capabilities" '代码与架构审查' cn-east 68 98)
agent_claude_triage=$(ensure_agent claude triage 'Claude 客诉研判员' 'claude-code' "$common_capabilities" '问题研判' cn-north 82 97)
agent_claude_backup=$(ensure_agent claude backup 'Claude 知识迁移助手' 'claude-code' "$common_capabilities" '知识整理' cn-south 45 99)
agent_codebuddy=$(ensure_agent codebuddy primary 'CodeBuddy 交付助手' 'codebuddy' "$common_capabilities" '交付编排' cn-east 73 98)
agent_codebuddy_support=$(ensure_agent codebuddy support 'CodeBuddy 客户修复助手' 'codebuddy' "$common_capabilities" '客户修复' cn-north 88 96)
agent_codebuddy_backup=$(ensure_agent codebuddy backup 'CodeBuddy 测试生成器' 'codebuddy' "$common_capabilities" '测试生成' cn-south 52 98)
agent_gemini=$(ensure_agent gemini primary 'Gemini 数据分析师' 'gemini-cli' "$common_capabilities" '数据分析' cn-east 64 99)
agent_gemini_quality=$(ensure_agent gemini quality 'Gemini 质量度量员' 'gemini-cli' "$common_capabilities" '质量复核' cn-north 58 98)
agent_gemini_backup=$(ensure_agent gemini backup 'Gemini 成本观察员' 'gemini-cli' "$common_capabilities" '成本分析' cn-south 41 99)
agent_cursor=$(ensure_agent cursor primary 'Cursor 故障修复 Agent' 'cursor-agent' "$common_capabilities" '代码修复' cn-east 79 97)
agent_cursor_mobile=$(ensure_agent cursor mobile 'Cursor 移动端 Agent' 'cursor-agent' "$common_capabilities" '多端开发' cn-north 67 98)
agent_cursor_backup=$(ensure_agent cursor backup 'Cursor 灰度验证 Agent' 'cursor-agent' "$common_capabilities" '灰度验证' cn-south 47 98)
agent_opencode=$(ensure_agent opencode primary 'OpenCode 合规执行器' 'opencode' "$common_capabilities" '合规门禁' cn-east 71 99)
agent_opencode_security=$(ensure_agent opencode security 'OpenCode 安全扫描器' 'opencode' "$common_capabilities" '安全扫描' cn-north 84 97)
agent_opencode_backup=$(ensure_agent opencode backup 'OpenCode 供应链审计器' 'opencode' "$common_capabilities" '供应链审计' cn-south 49 98)

bind_agent() {
  task_id=$1
  agent_id=$2
  resume_mode=$3
  capabilities=$4
  if ! api GET "/v1/tasks/$task_id/agents" | jq -e --arg id "$agent_id" '.agent_bindings[] | select(.agent_id == $id)' >/dev/null; then
    body=$(jq -nc --arg agent "$agent_id" --arg resume "$resume_mode" --argjson capabilities "$capabilities" '{agent_id:$agent,role:"contributor",resume_mode:$resume,capabilities:$capabilities,metadata:{managed_by:"demo-seed"}}')
    api POST "/v1/tasks/$task_id/agents" "$body" >/dev/null
  fi
}

write_demo_log() {
  provider=$1
  log_file=$2
  workspace=$3
  title=$4
  progress=$5
  artifact_path=$6
  metrics_path=$7
  session_id="demo-$provider-$progress"
  if [ "$provider" = codex ]; then
    jq -nc --arg id "$session_id" --arg cwd "$workspace" '{type:"session_meta",timestamp:"2026-08-31T02:00:00Z",payload:{id:$id,cwd:$cwd}}' >"$log_file"
    jq -nc --arg text "$title" '{type:"response_item",timestamp:"2026-08-31T02:01:00Z",payload:{id:"user-1",type:"message",role:"user",content:[{type:"input_text",text:$text}]}}' >>"$log_file"
    jq -nc '{type:"response_item",timestamp:"2026-08-31T02:02:00Z",payload:{id:"call-1",type:"function_call",name:"exec_command",arguments:"{}"}}' >>"$log_file"
    jq -nc '{type:"response_item",timestamp:"2026-08-31T02:03:00Z",payload:{id:"result-1",type:"function_call_output",output:"检查通过"}}' >>"$log_file"
    jq -nc --argjson progress "$progress" '{type:"response_item",timestamp:"2026-08-31T02:04:00Z",payload:{id:"assistant-1",type:"message",role:"assistant",content:[{type:"output_text",text:("任务已推进到 " + ($progress|tostring) + "%")}]}}' >>"$log_file"
    jq -nc '{type:"response_item",timestamp:"2026-08-31T02:04:30Z",payload:{id:"assistant-2",type:"message",role:"assistant",content:[{type:"output_text",text:"已记录跨 Agent 接力决策与恢复检查点"}]}}' >>"$log_file"
    jq -nc --arg path "$artifact_path" --arg metrics "$metrics_path" '{type:"item.completed",timestamp:"2026-08-31T02:05:00Z",item:{id:"file-1",type:"file_change",changes:[{path:$path,kind:"update"},{path:$metrics,kind:"create"}]}}' >>"$log_file"
  else
    case "$provider" in
      claude) session_type=system ;;
      codebuddy) session_type=conversation.started ;;
      gemini) session_type=init ;;
      cursor) session_type=composer.started ;;
      opencode) session_type=server.connected ;;
    esac
    jq -nc --arg type "$session_type" --arg id "$session_id" --arg cwd "$workspace" '{type:$type,session_id:$id,cwd:$cwd}' >"$log_file"
    jq -nc --arg title "$title" '{type:"message",id:"user-1",role:"user",timestamp:"2026-08-31T02:01:00Z",payload:{text:$title}}' >>"$log_file"
    jq -nc '{type:"tool_use",id:"call-1",timestamp:"2026-08-31T02:02:00Z",payload:{name:"workspace.inspect"}}' >>"$log_file"
    jq -nc '{type:"tool_result",id:"result-1",timestamp:"2026-08-31T02:03:00Z",payload:{status:"ok"}}' >>"$log_file"
    jq -nc --argjson progress "$progress" '{type:"assistant_message",id:"agent-1",timestamp:"2026-08-31T02:04:00Z",payload:{text:("任务已推进到 " + ($progress|tostring) + "%")}}' >>"$log_file"
    jq -nc --argjson progress "$progress" '{type:"progress",id:"status-1",timestamp:"2026-08-31T02:04:30Z",payload:{progress:$progress,status:"active"}}' >>"$log_file"
    jq -nc --arg path "$artifact_path" '{type:"file_change",id:"file-1",timestamp:"2026-08-31T02:05:00Z",file_path:$path,payload:{operation:"updated"}}' >>"$log_file"
    jq -nc --arg path "$metrics_path" '{type:"file_change",id:"file-2",timestamp:"2026-08-31T02:05:30Z",file_path:$path,payload:{operation:"created"}}' >>"$log_file"
  fi
}

ensure_task() {
  key=$1; shift
  title=$1; shift
  objective=$1; shift
  priority=$1; shift
  progress=$1; shift
  provider=$1; shift
  secondary_provider=$1; shift
  primary_agent=$1; shift
  secondary_agent=$1; shift
  primary_capabilities=$1; shift
  team_id=$1; shift
  health=$1; shift
  stage=$1; shift
  scenario=$1; shift
  owner=$1; shift
  sla_hours=$1; shift
  sla_remaining=$1; shift
  recovery_rate=$1; shift
  handoffs=$1; shift
  region=$1; shift
  business_value=$1; shift
  last_activity=$1; shift
  event_volume=$1; shift
  artifact_count=$1
  task_id=$(api GET /v1/tasks | jq -r --arg key "$key" --arg seed "$seed_id" 'first(.tasks[] | select(.metadata.demo_seed == $seed and .metadata.demo_key == $key) | .task_id) // ""')
  if [ -z "$task_id" ]; then
    body=$(jq -nc --arg title "$title" --arg objective "$objective" --arg device "$device_id" --arg org "$organization_id" --arg team "$team_id" --arg seed "$seed_id" --arg key "$key" --arg priority "$priority" --arg provider "$provider" --arg secondary "$secondary_provider" --arg health "$health" --arg stage "$stage" --arg scenario "$scenario" --arg owner "$owner" --arg region "$region" --arg business "$business_value" --argjson progress "$progress" --argjson sla_hours "$sla_hours" --argjson sla_remaining "$sla_remaining" --argjson recovery "$recovery_rate" --argjson handoffs "$handoffs" --argjson last_activity "$last_activity" --argjson event_volume "$event_volume" --argjson artifact_count "$artifact_count" '{title:$title,objective:$objective,origin_device:$device,organization_id:$org,team_id:$team,metadata:{demo_seed:$seed,demo_key:$key,priority:$priority,progress:$progress,primary_provider:$provider,secondary_provider:$secondary,health_status:$health,workflow_stage:$stage,scenario:$scenario,owner:$owner,sla_hours:$sla_hours,sla_remaining_minutes:$sla_remaining,resume_success_rate:$recovery,cross_agent_handoffs:$handoffs,region:$region,business_value:$business,last_activity_minutes:$last_activity,event_volume:$event_volume,artifact_count:$artifact_count}}')
    task_id=$(api POST /v1/tasks "$body" | jq -r '.task_id')
  fi
  bind_agent "$task_id" "$primary_agent" "$( [ "$provider" = codex ] && printf native || printf rehydrate )" "$primary_capabilities"
  bind_agent "$task_id" "$secondary_agent" rehydrate "$common_capabilities"
  event_count=$(api GET "/v1/tasks/$task_id/events?include_payload=false" | jq '.events | length')
  if [ "$event_count" -eq 0 ]; then
    workspace="$temporary_dir/workspace-$key"
    log_root="$temporary_dir/logs-$key"
    mkdir -p "$workspace" "$log_root"
    artifact_path="$workspace/运营交付-$key.md"
    metrics_path="$workspace/连续性指标-$key.json"
    printf '# %s\n\n目标：%s\n\n当前进度：%s%%\n\n该文件为非敏感本地演示产物。\n' "$title" "$objective" "$progress" >"$artifact_path"
    jq -nc --arg scenario "$scenario" --arg health "$health" --arg primary "$provider" --arg secondary "$secondary_provider" --argjson progress "$progress" --argjson recovery "$recovery_rate" --argjson handoffs "$handoffs" '{scenario:$scenario,health:$health,progress:$progress,route:[$primary,$secondary],resume_success_rate:$recovery,cross_agent_handoffs:$handoffs}' >"$metrics_path"
    log_file="$log_root/$provider-$key.jsonl"
    write_demo_log "$provider" "$log_file" "$workspace" "$title" "$progress" "$artifact_path" "$metrics_path"
    "$cli" adapter capture "$provider" "$log_root" "$log_file" --workspace "$workspace" --task "$task_id" --commit --agent-id "$primary_agent" --plaintext >/dev/null
  fi
  printf '%s\n' "$task_id"
}

task_release=$(ensure_task release-orchestration '多 Agent 发布编排' 'Codex 完成实现后交给 Claude 审查并保留可恢复上下文。' high 65 codex claude "$agent_codex" "$agent_claude" "$codex_capabilities" "$team_engineering" healthy review '软件发布' '平台工程组' 24 220 99 2 cn-east '高' 4 6 1)
task_customer=$(ensure_task customer-escalation '客户问题智能升级' 'Claude 归纳问题，CodeBuddy 接手修复并跟踪 24 小时 SLA。' critical 40 claude codebuddy "$agent_claude" "$agent_codebuddy" "$common_capabilities" "$team_customer" attention executing '客户服务' '客户成功组' 4 48 96 3 cn-north '高' 2 6 1)
task_delivery=$(ensure_task delivery-readiness 'CodeBuddy 交付就绪检查' 'CodeBuddy 汇总测试与产物，Gemini 复核质量指标。' high 80 codebuddy gemini "$agent_codebuddy" "$agent_gemini" "$common_capabilities" "$team_quality" healthy approval '质量交付' '质量保障组' 12 360 99 2 cn-east '高' 8 6 1)
task_cost=$(ensure_task cost-optimization '数据管道成本优化' 'Gemini 分析用量，Cursor 执行代码级优化并记录决策。' medium 55 gemini cursor "$agent_gemini" "$agent_cursor" "$common_capabilities" "$team_data" healthy executing '数据与成本' '数据智能组' 48 1260 98 1 cn-south '中' 12 6 1)
task_compliance=$(ensure_task compliance-release '合规发布门禁' 'OpenCode 执行合规清单，Codex 处理最终发布修复。' high 92 opencode codex "$agent_opencode" "$agent_codex" "$common_capabilities" "$team_security" attention approval '安全合规' '安全合规组' 8 95 97 2 cn-east '高' 3 6 1)

seed_wave() {
  wave_number=$1
  wave=$(printf '%02d' "$wave_number")
  case "$wave_number" in
    1) release_health=healthy; customer_health=attention; delivery_health=healthy; cost_health=healthy; security_health=attention; mobile_health=healthy; knowledge_health=completed; incident_health=attention ;;
    2) release_health=healthy; customer_health=healthy; delivery_health=completed; cost_health=healthy; security_health=healthy; mobile_health=attention; knowledge_health=completed; incident_health=blocked ;;
    3) release_health=attention; customer_health=blocked; delivery_health=healthy; cost_health=healthy; security_health=attention; mobile_health=healthy; knowledge_health=healthy; incident_health=attention ;;
    4) release_health=healthy; customer_health=attention; delivery_health=completed; cost_health=attention; security_health=blocked; mobile_health=healthy; knowledge_health=completed; incident_health=healthy ;;
    5) release_health=completed; customer_health=healthy; delivery_health=healthy; cost_health=healthy; security_health=healthy; mobile_health=completed; knowledge_health=completed; incident_health=attention ;;
    6) release_health=blocked; customer_health=attention; delivery_health=attention; cost_health=healthy; security_health=attention; mobile_health=healthy; knowledge_health=healthy; incident_health=blocked ;;
  esac
  progress_release=$((48 + wave_number * 7))
  progress_customer=$((31 + wave_number * 8))
  progress_delivery=$((57 + wave_number * 6))
  progress_cost=$((35 + wave_number * 7))
  progress_security=$((42 + wave_number * 7))
  progress_mobile=$((39 + wave_number * 8))
  progress_knowledge=$((64 + wave_number * 5))
  progress_incident=$((22 + wave_number * 9))
  recovery=$((93 + wave_number))
  last_activity=$((wave_number * 3))

  ensure_task "global-release-$wave" "全球发布波次 $wave" '在桌面端实现功能，由 Claude 完成架构审查，并在发布节点恢复全部上下文。' high "$progress_release" codex claude "$agent_codex" "$agent_claude" "$codex_capabilities" "$team_engineering" "$release_health" review '软件发布' '平台工程组' 24 $((760 - wave_number * 95)) "$recovery" $((wave_number % 4 + 2)) cn-east '高' "$last_activity" 7 2 >/dev/null
  ensure_task "customer-escalation-$wave" "重点客户升级单 C$wave" 'Claude 归纳多渠道客户上下文，CodeBuddy 接力修复并维持 SLA 与审计链。' critical "$progress_customer" claude codebuddy "$agent_claude_triage" "$agent_codebuddy_support" "$common_capabilities" "$team_customer" "$customer_health" executing '客户服务' '客户成功组' 4 $((260 - wave_number * 58)) $((91 + wave_number)) $((wave_number % 3 + 2)) cn-north '高' $((last_activity - 1)) 7 2 >/dev/null
  ensure_task "delivery-quality-$wave" "交付质量门禁 Q$wave" 'CodeBuddy 汇总测试证据与构建产物，Gemini 在不同设备上复核质量指标。' high "$progress_delivery" codebuddy gemini "$agent_codebuddy_backup" "$agent_gemini_quality" "$common_capabilities" "$team_quality" "$delivery_health" approval '质量交付' '质量保障组' 12 $((540 - wave_number * 45)) "$recovery" 2 cn-east '中' $((last_activity + 4)) 7 2 >/dev/null
  ensure_task "data-cost-$wave" "数据管道降本批次 D$wave" 'Gemini 分析用量与费用，Cursor 在保留决策上下文后执行代码级优化。' medium "$progress_cost" gemini cursor "$agent_gemini_backup" "$agent_cursor" "$common_capabilities" "$team_data" "$cost_health" executing '数据与成本' '数据智能组' 48 $((2100 - wave_number * 120)) $((94 + wave_number)) $((wave_number % 2 + 1)) cn-south '中' $((last_activity + 7)) 7 2 >/dev/null
  ensure_task "security-gate-$wave" "供应链安全门禁 S$wave" 'OpenCode 检查依赖与策略，Codex 接手修复，并保留签名事件与恢复检查点。' high "$progress_security" opencode codex "$agent_opencode_security" "$agent_codex_review" "$common_capabilities" "$team_security" "$security_health" approval '安全合规' '安全合规组' 8 $((430 - wave_number * 78)) $((92 + wave_number)) $((wave_number % 4 + 1)) cn-east '高' $((last_activity + 1)) 7 2 >/dev/null
  ensure_task "mobile-handoff-$wave" "移动端跨设备接力 M$wave" 'Cursor 在 macOS 开发，Codex 在移动设备反馈后恢复上下文并完成跨端修复。' high "$progress_mobile" cursor codex "$agent_cursor_mobile" "$agent_codex" "$common_capabilities" "$team_product" "$mobile_health" executing '多端协作' '产品体验组' 16 $((820 - wave_number * 82)) "$recovery" $((wave_number % 5 + 1)) cn-north '高' $((last_activity + 2)) 7 2 >/dev/null
  ensure_task "knowledge-migration-$wave" "知识库迁移批次 K$wave" 'Claude 提炼历史任务决策，Gemini 验证结构化知识与产物引用的一致性。' medium "$progress_knowledge" claude gemini "$agent_claude_backup" "$agent_gemini" "$common_capabilities" "$team_data" "$knowledge_health" observing '知识迁移' '数据智能组' 72 $((3100 - wave_number * 180)) $((95 + wave_number % 4)) $((wave_number % 3 + 1)) cn-south '中' $((last_activity + 13)) 7 2 >/dev/null
  ensure_task "production-incident-$wave" "生产故障协同 INC-$wave" 'Cursor 现场止损，Claude 复盘根因；任一 Agent 中断后都能从签名事件恢复。' critical "$progress_incident" cursor claude "$agent_cursor_backup" "$agent_claude" "$common_capabilities" "$team_engineering" "$incident_health" executing '故障响应' '平台工程组' 2 $((145 - wave_number * 31)) $((89 + wave_number)) $((wave_number % 4 + 3)) cn-east '极高' "$wave_number" 7 2 >/dev/null
}

for wave_number in 1 2 3 4 5 6; do
  echo "Seeding operating wave $wave_number/6..."
  seed_wave "$wave_number"
done

ensure_api_key 'Demo · 只读运营看板' '["tasks:read","agents:read","notifications:read","usage:read"]'
ensure_api_key 'Demo · 自动化发布流水线' '["tasks:read","tasks:write","agents:read","audit:read"]'
ensure_api_key 'Demo · 安全审计导出' '["tasks:read","audit:read","organizations:read"]'
ensure_oauth_client 'Demo · 移动端 AIAgent App' '["tasks:read","tasks:write","agents:read","notifications:read"]'
ensure_oauth_client 'Demo · Web 工作台' '["tasks:read","agents:read","organizations:read"]'
ensure_oauth_client 'Demo · 企业自动化中台' '["tasks:read","tasks:write","agents:read","usage:read"]'

if ! api GET /v1/billing/accounts | jq -e --arg org "$organization_id" '.billing_accounts[] | select(.organization_id == $org)' >/dev/null; then
  api POST /v1/billing/accounts "$(jq -nc --arg org "$organization_id" '{organization_id:$org,plan:"team",currency:"CNY"}')" >/dev/null
fi

ensure_listing() {
  name=$1
  kind=$2
  price=$3
  listing_id=$(api GET '/v1/marketplace/listings?status=published' | jq -r --arg name "$name" 'first(.listings[] | select(.name == $name) | .listing_id) // ""')
  if [ -z "$listing_id" ]; then
    body=$(jq -nc --arg name "$name" --arg kind "$kind" --arg seed "$seed_id" --argjson price "$price" '{kind:$kind,name:$name,description:"可直接安装到 Agent Continuity 工作区的本地运营演示能力。",version:"1.0.0",capabilities:["continuity","rehydrate","signed_events","artifacts"],manifest:{demo_seed:$seed},price_minor:$price,currency:"CNY",commission_basis_points:500,status:"published"}')
    listing_id=$(api POST /v1/marketplace/listings "$body" | jq -r '.listing_id')
  fi
  printf %s "$listing_id"
}

ensure_installation() {
  listing_id=$1
  if ! api GET /v1/marketplace/installations | jq -e --arg id "$listing_id" '.installations[] | select(.listing_id == $id and .status == "active")' >/dev/null; then
    api POST "/v1/marketplace/listings/$listing_id/install" >/dev/null
  fi
}

listing_claude=$(ensure_listing 'Claude 审查能力包' capability 990)
listing_codebuddy=$(ensure_listing 'CodeBuddy 交付适配器' adapter 0)
listing_release=$(ensure_listing '跨 Agent 发布任务模板' task 1990)
listing_mobile=$(ensure_listing '移动端连续性 SDK 模板' task 0)
listing_security=$(ensure_listing '供应链安全门禁能力包' capability 1290)
listing_incident=$(ensure_listing '生产故障接力模板' task 1590)
listing_gemini=$(ensure_listing 'Gemini 数据质量适配器' adapter 0)
listing_cursor=$(ensure_listing 'Cursor 多端恢复适配器' adapter 0)
listing_opencode=$(ensure_listing 'OpenCode 合规策略包' capability 890)
listing_cost=$(ensure_listing 'Agent 成本优化仪表板' capability 590)
listing_knowledge=$(ensure_listing '任务知识迁移模板' task 0)
listing_e2ee=$(ensure_listing '端到端加密协作模板' task 2490)
: "$listing_claude" "$listing_release" "$listing_security" "$listing_incident" "$listing_opencode" "$listing_cost" "$listing_e2ee"
ensure_installation "$listing_codebuddy"
ensure_installation "$listing_mobile"
ensure_installation "$listing_gemini"
ensure_installation "$listing_cursor"
ensure_installation "$listing_knowledge"

demo_task_count=$(api GET /v1/tasks | jq --arg seed "$seed_id" '[.tasks[] | select(.metadata.demo_seed == $seed)] | length')
demo_agent_count=$(api GET /v1/agents | jq --arg seed "$seed_id" '[.agents[] | select(.card.demo_seed == $seed and .revoked_at == null)] | length')
demo_device_count=$(api GET /v1/devices | jq '.devices | length')
demo_team_count=$(api GET "/v1/organizations/$organization_id/teams" | jq --arg seed "$seed_id" '[.teams[] | select(.settings.demo_seed == $seed)] | length')
demo_listing_count=$(api GET '/v1/marketplace/listings?status=published' | jq --arg seed "$seed_id" '[.listings[] | select(.manifest.demo_seed == $seed)] | length')
demo_event_estimate=$(api GET /v1/tasks | jq --arg seed "$seed_id" '[.tasks[] | select(.metadata.demo_seed == $seed) | (.metadata.event_volume // 6)] | add // 0')
demo_artifact_estimate=$(api GET /v1/tasks | jq --arg seed "$seed_id" '[.tasks[] | select(.metadata.demo_seed == $seed) | (.metadata.artifact_count // 1)] | add // 0')

jq -nc \
  --arg account_id "$account_id" \
  --arg organization_id "$organization_id" \
  --arg credentials_file "$credentials_file" \
  --arg task_release "$task_release" \
  --arg task_customer "$task_customer" \
  --arg task_delivery "$task_delivery" \
  --arg task_cost "$task_cost" \
  --arg task_compliance "$task_compliance" \
  --argjson task_count "$demo_task_count" \
  --argjson agent_count "$demo_agent_count" \
  --argjson device_count "$demo_device_count" \
  --argjson team_count "$demo_team_count" \
  --argjson listing_count "$demo_listing_count" \
  --argjson event_count "$demo_event_estimate" \
  --argjson artifact_count "$demo_artifact_estimate" \
  '{status:"seeded",account_id:$account_id,organization_id:$organization_id,credentials_file:$credentials_file,demo_scale:{tasks:$task_count,agents:$agent_count,devices:$device_count,teams:$team_count,marketplace_listings:$listing_count,events:$event_count,artifacts:$artifact_count},featured_tasks:[$task_release,$task_customer,$task_delivery,$task_cost,$task_compliance]}'
