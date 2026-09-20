#!/usr/bin/env bash
# 在 LMBY 开发容器里跑：解包 → 校验新代码 → gofmt → build → vet → test
#
# 为什么要有这个脚本：本机 Windows 上没装 Go，编译与测试都在容器里做。
# 而「往 ssh 命令行里塞引号」在 PowerShell 下必翻车（$(...) 与 \" 会被本机
# shell 先吃掉，`grep -c "x" file` 会变成 grep 三个参数），所以固化成
# 「上传脚本 → bash 执行」这一条路。
#
# Windows 侧：
#   tar czf $env:TEMP\lmby-src.tgz --exclude=./.git --exclude=./web/node_modules -C . .
#   node ssh.mjs put $env:TEMP\lmby-src.tgz /root/lmby-src.tgz
#   node ssh.mjs put scripts/dev/container-verify.sh /root/cv.sh
#   Start-Sleep -Seconds 2      # 等 sftp 真正落盘（立刻比对会读到半截文件）
#   node ssh.mjs exec "tr -d '\r' < /root/cv.sh > /tmp/cv.sh && bash /tmp/cv.sh"
#
# 环境变量：SRC（默认 /root/lmby-src.tgz）、DST（默认 /opt/lmby）、
#           EXPECT_MD5（可选的包校验值，与本机 Get-FileHash 的结果对比）
set -o pipefail

SRC=${SRC:-/root/lmby-src.tgz}
DST=${DST:-/opt/lmby}
GOBIN=${GOBIN:-/usr/local/go/bin/go}
GOFMT=${GOFMT:-/usr/local/go/bin/gofmt}
export GOPROXY=${GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}

echo "== 包 md5 =="
gotmd5=$(md5sum "$SRC" | cut -d' ' -f1)
echo "$gotmd5"
if [ -n "$EXPECT_MD5" ] && [ "$gotmd5" != "$EXPECT_MD5" ]; then
  echo "!! 上传包的 md5 与本机不符（期望 $EXPECT_MD5），可能没传完/传错，停止"
  exit 1
fi

echo "== 解包到 $DST =="
rm -rf "$DST"
mkdir -p "$DST"
tar xzf "$SRC" -C "$DST" || exit 1

# 防「上传/解包静默失败」的解出来的还是旧代码：
# 光看 tar 退出码不够（踩过），要看一个本次新增/改动过的文件真的在。
echo "== 校验解出来的是新代码 =="
for f in cmd/lmby/main.go internal/match/score.go cmd/lmby/match.go; do
  if [ ! -s "$DST/$f" ]; then
    echo "!! 缺少 $f，解出来的不是预期代码，停止"
    exit 1
  fi
done
echo "ok"

cd "$DST" || exit 1

echo "== gofmt 检查 =="
rm -rf /root/fmt-pull
mkdir -p /root/fmt-pull
"$GOFMT" -l . > /tmp/fmt.txt || true
if [ -s /tmp/fmt.txt ]; then
  echo "发现未格式化的文件（CI 的 lint 任务会因此变红，必须修）："
  cat /tmp/fmt.txt
  while read -r f; do
    [ -z "$f" ] && continue
    "$GOFMT" -w "$f"
    mkdir -p "/root/fmt-pull/$(dirname "${f#./}")"
    cp "$f" "/root/fmt-pull/${f#./}"
    echo "  已格式化并复制到 /root/fmt-pull/：$f"
  done < /tmp/fmt.txt
else
  echo "gofmt 干净"
fi

echo "== go build =="
"$GOBIN" build -trimpath -o /tmp/lmby.new ./cmd/lmby || exit 1
echo BUILD-OK

echo "== go vet =="
"$GOBIN" vet ./... || exit 1
echo VET-OK

echo "== go test ./... =="
"$GOBIN" test -count=1 ./... || exit 1

echo ALL-DONE
