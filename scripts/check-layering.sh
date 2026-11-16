#!/usr/bin/env bash
# 分层依赖方向检查（架构纲要 §2.3）。
#
# 规则：
#   llm     不得依赖任何其他内部包（契约根，只允许标准库）
#   infra   不得依赖 llm / provider / session / cost / store / bootstrap
#
# 直接依赖（check_deny_direct / warn_deny_direct）看的是 import 本身，
# 不是传递闭包。带 /* 的规则匹配该前缀下的全部子包。
# warn_deny_direct 只打印「待治理」，不让检查失败——已知债先可见，
# 治理完成后再改成 check_deny_direct。
#
# 用法：scripts/check-layering.sh
# 退出码：0 = 通过，1 = 违规。

set -euo pipefail

MODULE="github.com/lincanyitse/baiyi-chat"
failed=0

fail() {
  echo "分层违规: $1"
  failed=1
}

check_no_internal_deps() {
  local pkg="$1"
  local deps
  deps=$(go list -deps "$MODULE/internal/$pkg" 2>/dev/null | grep "^$MODULE/internal/" || true)
  # go list -deps 会包含自身，排除掉
  deps=$(echo "$deps" | grep -v "^$MODULE/internal/$pkg$" || true)
  if [ -n "$deps" ]; then
    fail "internal/$pkg 不得依赖内部包，实际依赖："
    echo "$deps" | sed 's/^/    /'
  fi
}

check_deny() {
  local pkg="$1"
  shift
  for denied in "$@"; do
    if go list -deps "$MODULE/internal/$pkg" 2>/dev/null | grep -qx "$MODULE/internal/$denied"; then
      fail "internal/$pkg 不得依赖 internal/$denied"
    fi
  done
}

# matches_denied 判断一条 import 是否命中规则。
# 规则以 /* 结尾时匹配该前缀下的全部子包，否则要求路径完全相等。
matches_denied() {
  local imp="$1"
  local pattern="$2"
  local root="$MODULE/internal"
  if [[ "$pattern" == *"/*" ]]; then
    local prefix="${pattern%/*}"
    [[ "$imp" == "$root/$prefix/"* ]]
  else
    [[ "$imp" == "$root/$pattern" ]]
  fi
}

# report_direct 检查 pkg 的直接 import。mode 为 fail 时记违规，为 warn 时只提示。
report_direct() {
  local mode="$1"
  local pkg="$2"
  shift 2
  local imports denied imp
  imports=$(go list -f '{{join .Imports "\n"}}' "$MODULE/internal/$pkg" 2>/dev/null || true)
  for denied in "$@"; do
    while IFS= read -r imp; do
      [ -z "$imp" ] && continue
      if matches_denied "$imp" "$denied"; then
        local msg="internal/$pkg 不得直接依赖 ${imp#"$MODULE/"}（规则: $denied）"
        if [ "$mode" = "warn" ]; then
          echo "待治理: $msg"
        else
          fail "$msg"
        fi
      fi
    done <<< "$imports"
  done
}

check_deny_direct() {
  report_direct fail "$@"
}

warn_deny_direct() {
  report_direct warn "$@"
}

echo "检查分层依赖方向..."

# llm 是契约根
check_no_internal_deps llm

# infra 是最底层
check_deny infra llm provider session cost store bootstrap channel provider/registry

# embed 是中立能力包，只依赖 llm；不得反向依赖 rag/memory/session。
# memory 与 rag 各自消费它，任何一侧被它依赖都会造成循环。
check_deny embed rag memory session bootstrap channel store jobs

# jobs 只认 Handler 接口，不得依赖具体业务实现。
check_deny jobs rag memory session bootstrap channel agent

# 渠道是最外层。内核不得反向 import 渠道；渠道不得直接碰到
# 运行时、存储实现与协议适配器（应经由 session 与契约类型）。
#
# provider/registry 对 channel/web 例外：Core 接口必须写出
# SessionModel 已有的 ModelRef 类型，否则 *session.Service 赋不进去。
# 具体协议适配器仍然禁止。
check_deny_direct agent 'channel/*'
check_deny_direct session 'channel/*'
check_deny_direct channel/web agent 'store/*' infra/sqlite provider provider/anthropic provider/openaicompat provider/responses provider/sse
check_deny_direct channel/repl agent 'store/*' infra/sqlite 'provider/*'

# 领域包不直连 sqlite。持久化细节留在 store，或经由本包自己的窄接口。
check_deny_direct session infra/sqlite
check_deny_direct cost infra/sqlite

# 已知债：export 是格式转换，却依赖 session 的类型。不在本批范围。
warn_deny_direct export session

if [ "$failed" -eq 0 ]; then
  echo "通过：分层依赖方向正确。"
else
  exit 1
fi
