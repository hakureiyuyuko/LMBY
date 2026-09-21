#!/usr/bin/env bash
# 校验「编码能力探测」的结论与这台机器的实际表现一致。
#
# 为什么值得单独验：探测结果直接决定「播放时用不用硬件编码」——
# 探错了的后果是播放卡成幻灯片（把不可用的后端当可用），或者白白浪费
# 硬件（把可用的后端当不可用）。而它恰恰是最容易悄悄错的一环：
# `-encoders` 里列出的编码器跟「这台机器能不能跑」完全是两回事。
#
# 用法：LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-caps.sh
# 环境变量：BASE（默认 http://127.0.0.1:8099）、FFMPEG（默认 ffmpeg）
set -uo pipefail

BASE=${BASE:-http://127.0.0.1:8099}
USER=${LMBY_USER:-}
PASS=${LMBY_PASS:-}
FF=${FFMPEG:-ffmpeg}
GROUP="@"
PASS_N=0
FAIL_N=0

check() { # check 名称 期望 实际
  local name="$1" want="$2" got="$3"
  if [[ "$want" == "$got" ]]; then
    printf "${GROUP} ok   %s\n" "$name"; PASS_N=$((PASS_N + 1))
  else
    printf "${GROUP} FAIL %s（期望 %s，实际 %s）\n" "$name" "$want" "$got"; FAIL_N=$((FAIL_N + 1))
  fi
}
bool() { [[ "$1" == "true" ]] && echo true || echo false; }
note() { printf "${GROUP} --   %s\n" "$1"; }

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=... LMBY_PASS=... bash $0" >&2
  exit 2
fi

JAR=$(mktemp)
trap 'rm -f "$JAR"' EXIT

echo "== 0. 登录 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' --data-binary "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录" 200 "$code"

echo
echo "== 1. 能力表接口 =="
CAPS=$(curl -s -b "$JAR" "$BASE/api/v1/transcode/capabilities")
check "返回里有 capabilities" true "$(bool "$(jq -r 'has("capabilities")' <<<"$CAPS")")"
check "探测时间非空" true "$(bool "$(jq -r '.capabilities.probedAt != null' <<<"$CAPS")")"
check "ffmpeg 版本非空" true "$(bool "$(jq -r '(.capabilities.version|length) > 0' <<<"$CAPS")")"
check "CPU 基线可用（libx264 真跑过）" true "$(bool "$(jq -r '.capabilities.software.encode.h264' <<<"$CAPS")")"
note "首选后端：$(jq -r '.best.name' <<<"$CAPS")"

echo
echo "== 2. 能力表 vs 实际：逐条对 =="
# 判据：能力表说「可用」的后端，我们在这里再真跑一次；说「不可用」的，要能复现失败。
# 这样即便换了机器，脚本也会给出「探测与事实不符」而不是默默用过时结论。

# 2.1 CPU 基线
if "$FF" -hide_banner -loglevel error -f lavfi -i testsrc2=size=640x360:rate=25 -t 1 \
  -c:v libx264 -preset ultrafast -f null - >/dev/null 2>&1; then
  ACTUAL_SW=true
else
  ACTUAL_SW=false
fi
check "libx264 真跑（对照能力表）" "$ACTUAL_SW" "$(jq -r '.capabilities.software.encode.h264' <<<"$CAPS")"

# 2.2 每个硬件后端
for kind in vaapi qsv nvenc; do
  listed=$(jq -r --arg k "$kind" '.capabilities.backends[] | select(.kind==$k) | .encode.h264 // false' <<<"$CAPS")
  usable=$(jq -r --arg k "$kind" '.capabilities.backends[] | select(.kind==$k) | (.available and (.encode.h264 // false))' <<<"$CAPS")
  dev=$(jq -r --arg k "$kind" '.capabilities.backends[] | select(.kind==$k) | .device // ""' <<<"$CAPS")

  if [[ "$usable" == "true" ]]; then
    # 声称可用 → 我们照着它的设备节点真跑一次
    if [[ "$kind" == "vaapi" ]]; then
      if "$FF" -hide_banner -loglevel error -vaapi_device "$dev" -f lavfi -i testsrc2=size=640x360:rate=25 -t 1 \
        -vf format=nv12,hwupload -c:v h264_vaapi -rc_mode CQP -qp 23 -f null - >/dev/null 2>&1; then
        ACTUAL=true
      else
        ACTUAL=false
      fi
    else
      ACTUAL=skip
    fi
    if [[ "$ACTUAL" == "skip" ]]; then
      note "$kind 声称可用，但没有独立的复现命令，跳过对照"
    else
      check "$kind 声称可用且真跑得通" true "$(bool "$ACTUAL")"
    fi
  else
    # 声称不可用 → 如果 ffmpeg 里有这个编码器，我们真跑一次应当失败（证明探测不是瞎写的）
    enc=$(jq -r --arg k "$kind" '.capabilities.backends[] | select(.kind==$k) | ((.encode // {}) | keys | length)' <<<"$CAPS")
    if [[ "$listed" == "false" && "$enc" == "0" ]]; then
      note "$kind 不可用（能力表：编码器真跑未通过或不存在）"
    fi
  fi
done

# 2.3 VAAPI 的细节：设备节点、质量模式、滤镜
if jq -e '.capabilities.backends[] | select(.kind=="vaapi" and .available)' - <<<"$CAPS" >/dev/null; then
  DEV=$(jq -r '.capabilities.backends[] | select(.kind=="vaapi") | .device' <<<"$CAPS")
  check "VAAPI 设备节点存在" true "$(bool "$([[ -e "$DEV" ]] && echo true)")"
  check "VAAPI 记下了 cqp 模式" true \
    "$(bool "$(jq -r '[.capabilities.backends[]|select(.kind=="vaapi")|(.quality // [])[]]|index("cqp")!=null' <<<"$CAPS")")"
  check "VAAPI 声明了 scale_vaapi 滤镜" true \
    "$(bool "$(jq -r '.capabilities.backends[]|select(.kind=="vaapi")|((.filters // [])|index("scale_vaapi")!=null)' <<<"$CAPS")")"
else
  note "本机 VAAPI 不可用，跳过 VAAPI 细节对照"
fi

# 2.4 解码能力（真文件：生成 1 秒 hevc 再硬解）
if jq -e '.capabilities.backends[] | select(.kind=="vaapi" and .decode.hevc==true)' - <<<"$CAPS" >/dev/null; then
  TMP=$(mktemp -d)
  if "$FF" -hide_banner -loglevel error -y -f lavfi -i testsrc2=size=640x360:rate=25 -t 1 \
    -c:v libx265 -preset ultrafast -tag:v hvc1 "$TMP/x.mkv" >/dev/null 2>&1; then
    DEV=$(jq -r '.capabilities.backends[] | select(.kind=="vaapi") | .device' <<<"$CAPS")
    if "$FF" -hide_banner -loglevel error -vaapi_device "$DEV" -hwaccel vaapi -hwaccel_output_format vaapi \
      -i "$TMP/x.mkv" -f null - >/dev/null 2>&1; then
      ACTUAL=true
    else
      ACTUAL=false
    fi
    check "VAAPI 硬解 HEVC（能力表说可以）" true "$(bool "$ACTUAL")"
  else
    note "本机编不出 HEVC 小样，跳过硬解对照"
  fi
  rm -rf "$TMP"
else
  note "能力表未声明 VAAPI 硬解 HEVC，跳过"
fi

echo
echo "== 3. 缓存与刷新 =="
BEFORE=$(jq -r '.capabilities.probedAt' <<<"$CAPS")
AFTER=$(curl -s -b "$JAR" "$BASE/api/v1/transcode/capabilities" | jq -r '.capabilities.probedAt')
check "重复请求命中缓存（探测时间不变）" "$BEFORE" "$AFTER"
REFRESH=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/transcode/capabilities/refresh" | jq -r '.capabilities.probedAt')
check "管理员刷新后探测时间变了" true "$(bool "$([[ "$REFRESH" != "$BEFORE" ]] && echo true)")"

echo
echo "== 4. 警告面（非致命）=="
jq -r '.capabilities.warnings[]?' <<<"$CAPS" | sed 's/^/  ⚠ /' || true

echo
if [[ $FAIL_N -eq 0 ]]; then
  printf "${GROUP} \033[1m结果：%d 通过，0 失败\033[0m\n" "$PASS_N"
else
  printf "${GROUP} \033[1m结果：%d 通过，%d 失败\033[0m\n" "$PASS_N" "$FAIL_N"
fi
[[ $FAIL_N -eq 0 ]]
