#!/usr/bin/env bash
# 本机打包 aegis-dns 发布包（京东云拉不动 GitHub，必须离线传）
set -euo pipefail
export PATH=$PATH:/usr/local/go/bin
export GOPROXY=https://goproxy.cn,direct

VERSION="${1:-1.0.5}"
ROOT=/root/aegis-dns
OUT=/root/aegis-rel-$VERSION
PKG="aegis-dns-$VERSION-linux-amd64"

echo "==> 1/5 构建前端"
cd "$ROOT/web"
npm run build >/dev/null 2>&1 || { echo "前端构建失败"; exit 1; }
echo "    dist: $(find dist -type f | wc -l) 个文件"

echo "==> 2/5 内嵌前端到 apid"
# 保留 .gitkeep，否则构建后 git 会认为该目录被删除
mkdir -p "$ROOT/server/internal/api/webdist/dist"
find "$ROOT/server/internal/api/webdist/dist" -mindepth 1 ! -name .gitkeep -delete
cp -r dist/* "$ROOT/server/internal/api/webdist/dist/"

echo "==> 3/5 交叉编译"
cd "$ROOT/server"
LDF="-s -w -X github.com/50521136/aegis-dns/server/internal/version.Version=$VERSION"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDF" -o /tmp/aegis-dnsd ./cmd/dnsd
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDF" -o /tmp/aegis-apid ./cmd/apid
echo "    dnsd: $(du -h /tmp/aegis-dnsd | cut -f1)  apid: $(du -h /tmp/aegis-apid | cut -f1)"

echo "==> 4/5 组装发布包"
rm -rf "$OUT"; mkdir -p "$OUT/$PKG"
cp /tmp/aegis-dnsd /tmp/aegis-apid "$OUT/$PKG/"
cp "$ROOT/deploy/install.sh" "$ROOT/deploy/selfcheck.sh" "$ROOT/deploy/dnsprobe.py" \
   "$ROOT/deploy/verify-deployed.sh" "$ROOT/deploy/verify-global-layer.sh" \
   "$ROOT/deploy/dot-android-probe.py" "$OUT/$PKG/" 2>/dev/null || true
cp "$ROOT/deploy/aegis-dnsd.service" "$ROOT/deploy/aegis-apid.service" "$OUT/$PKG/"
chmod +x "$OUT/$PKG"/aegis-* "$OUT/$PKG"/*.sh

echo "==> 5/5 校验"
cd "$OUT"
sha256sum "$PKG"/* > "$PKG/checksums.txt"
tar czf "$PKG.tar.gz" "$PKG"
echo "    包: $OUT/$PKG.tar.gz  ($(du -h "$OUT/$PKG.tar.gz" | cut -f1))"
echo "    版本: $(/tmp/aegis-apid -version 2>/dev/null || echo "$VERSION")"
rm -f /tmp/aegis-dnsd /tmp/aegis-apid
