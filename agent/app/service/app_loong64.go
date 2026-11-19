package service

import (
	"errors"
	"runtime"
	"strings"

	"github.com/1Panel-dev/1Panel/agent/global"
)

// On linux/loong64 hosts most Docker Hub images cannot be pulled because
// they ship no loong64 manifest. This layer rewrites appstore image
// references to loong64-capable equivalents:
//   - lcr.loongnix.cn (Loongson community registry, which rebuilds popular
//     images for loong64)
//   - a local registry (127.0.0.1:5000/loong/...) for images we build on
//     the device (e.g. OpenResty)
//
// All rules are keyed by the source repository path (namespace/name or
// bare docker hub library name). Tag selection: ByMajor[<major>] wins,
// then Fallback; an empty Fallback keeps the requested tag.
// It is a no-op on every other architecture.

type loong64Rule struct {
	Target   string            // target repo path, registry prefix included
	Fallback string            // tag used when the requested tag should be replaced
	ByMajor  map[string]string // source major version -> target tag
}

const (
	loongLCRRegistry      = "lcr.loongnix.cn/"
	loongLocalRegistry    = "127.0.0.1:5000/loong/"
	loong64UnsupportedMsg = "该应用暂无 LoongArch64（龙芯）版本镜像，已列入移植矩阵不支持清单；可在「容器」页用自定义 compose 部署替代镜像"
)

// exact repository-path rules (source repo -> rule)
var loong64RepoRules = map[string]loong64Rule{
	// --- local self-built images (phase-2 batch 1: Go single-binary apps) ---
	"1panel/openresty":        {Target: loongLocalRegistry + "openresty"},   // keep tag
	"caddy":                   {Target: loongLocalRegistry + "caddy"},       // keep tag
	"filebrowser/filebrowser": {Target: loongLocalRegistry + "filebrowser"}, // keep tag
	"jeessy/ddns-go":          {Target: loongLocalRegistry + "ddns-go"},     // keep tag

	// --- local self-built images (phase-2 batch 2) ---
	"joyqi/typecho":        {Target: loongLocalRegistry + "typecho"},     // keep tag
	"louislam/uptime-kuma": {Target: loongLocalRegistry + "uptime-kuma"}, // keep tag

	// --- local self-built images (wave 2, 2026-11-07) ---
	"louislam/dockge": {Target: loongLocalRegistry + "dockge"}, // keep tag

	// --- local self-built images (wave 3, 2026-11-07) ---
	"neosmemo/memos":       {Target: loongLocalRegistry + "memos"},      // keep tag
	"twinproduction/gatus": {Target: loongLocalRegistry + "gatus"},      // keep tag
	"commitgo/gitea-ee":    {Target: loongLocalRegistry + "gitea"},      // keep tag; CE substitute
	"henrygd/beszel":       {Target: loongLocalRegistry + "beszel"},     // keep tag
	"prom/prometheus":      {Target: loongLocalRegistry + "prometheus"}, // keep tag
	"deluan/navidrome":     {Target: loongLocalRegistry + "navidrome"},  // keep tag
	"gotify/server":        {Target: loongLocalRegistry + "gotify"},     // keep tag; CGO sqlite build
	"hslr/sun-panel":       {Target: loongLocalRegistry + "sun-panel"},  // keep tag; CGO sqlite build

	// --- local self-built images (wave 4, 2026-11-07) ---
	"ghcr.io/thomiceli/opengist": {Target: loongLocalRegistry + "opengist"},      // keep tag
	"ghcr.io/screego/server":     {Target: loongLocalRegistry + "screego"},       // keep tag
	"mayswind/ezbookkeeping":     {Target: loongLocalRegistry + "ezbookkeeping"}, // keep tag
	"adguard/adguardhome":        {Target: loongLocalRegistry + "adguardhome"},   // keep tag

	// --- local self-built images (wave 6, 2026-11-08) ---
	"ghcr.io/komari-monitor/komari": {Target: loongLocalRegistry + "komari"},   // keep tag; device CGO build, web dist embedded
	"bitnamilegacy/openldap":        {Target: loongLocalRegistry + "openldap"}, // keep tag; debian sid slapd, bitnami env interface

	// --- local self-built images (wave 7, 2026-11-08) ---
	"casbin/casdoor":        {Target: loongLocalRegistry + "casdoor"}, // keep tag; store-version build, entrypoint maps env->conf/app.conf for DB selection
	"ghcr.io/nezhahq/nezha": {Target: loongLocalRegistry + "nezha"},   // keep tag; swag docs + dual-frontend embed, device CGO build

	// --- local self-built images (wave 8, 2026-11-08) ---
	"cloudreve/cloudreve":            {Target: loongLocalRegistry + "cloudreve"},         // keep tag (no v); pure-Go cross build, frontend assets.zip embedded
	"woodpeckerci/woodpecker-server": {Target: loongLocalRegistry + "woodpecker-server"}, // keep tag; device CGO sqlite, web/dist pnpm embed
	"woodpeckerci/woodpecker-agent":  {Target: loongLocalRegistry + "woodpecker-agent"},  // keep tag; same build, agent ENTRYPOINT
	"mattermost/focalboard":          {Target: loongLocalRegistry + "focalboard"},        // keep tag (no v); webapp pack PC build, server device CGO, CRLF strip on go files
	"vaultwarden/server":             {Target: loongLocalRegistry + "bitwarden"},         // keep tag (1.37.5-alpine); rust binary + official prebuilt web-vault, USER root + ROCKET_ADDRESS=0.0.0.0

	// --- local self-built images (wave 10, 2026-11-08) ---
	"excalidraw/excalidraw": {Target: loongLocalRegistry + "excalidraw"}, // keep tag (latest); yarn monorepo built in device container, esbuild resolution pinned
	"easytier/easytier":     {Target: loongLocalRegistry + "easytier"},   // keep tag; host rust 1.90 build, debian runtime
	// rustdesk-server moved to loong64BlockedRepos (wave 14 audit: build never landed, rule was dead)

	// --- local self-built images (wave 11, 2026-11-08) ---
	"imaegoo/twikoo":   {Target: loongLocalRegistry + "twikoo"}, // keep tag; correct server pkg is tkserver (twikoo npm pkg is client-only)
	"lizheming/waline": {Target: loongLocalRegistry + "waline"}, // keep tag; better-sqlite3 compiled in-image + schema auto-init entry

	// --- local self-built images (wave 12, 2026-11-08) ---
	"requarks/wiki": {Target: loongLocalRegistry + "wikijs"}, // keep tag; v3.1.0 official tarball, node26 TS-direct + temporal-polyfill preload (lcr node26 lacks Temporal), sharp optional dropped, PG>=17 runtime gate

	// --- local self-built images (wave 13, 2026-11-08) ---
	"advplyr/audiobookshelf": {Target: loongLocalRegistry + "audiobookshelf"}, // keep tag; nuxt2 client generate + tsc server + sqlite3 source build, node20 debian runtime + sid ffmpeg, libnusqlite3 omitted (graceful)

	// --- local self-built images (phase-2 batch 3) ---
	"halohub/halo-pro":     {Target: loongLocalRegistry + "halo-pro"}, // keep tag; community jar under pro tag
	"linuxserver/freshrss": {Target: loongLocalRegistry + "freshrss"}, // keep tag; official FreshRSS recipe, data at /config

	// --- local self-built images (mega216 batch, 2026-11-07) ---
	"cloudflare/cloudflared":         {Target: loongLocalRegistry + "cloudflared"},          // keep tag (mega216)
	"miniflux/miniflux":              {Target: loongLocalRegistry + "miniflux"},             // keep tag (mega216)
	"prom/node-exporter":             {Target: loongLocalRegistry + "node-exporter"},        // keep tag (mega216)
	"containrrr/watchtower":          {Target: loongLocalRegistry + "watchtower"},           // keep tag (mega216)
	"snowdreamtech/frpc":             {Target: loongLocalRegistry + "frpc"},                 // keep tag (mega216)
	"snowdreamtech/frps":             {Target: loongLocalRegistry + "frps"},                 // keep tag (mega216)
	"glanceapp/glance":               {Target: loongLocalRegistry + "glance"},               // keep tag (mega216)
	"foxcpp/maddy":                   {Target: loongLocalRegistry + "maddy-mail"},           // keep tag (mega216)
	"artalk/artalk-go":               {Target: loongLocalRegistry + "artalk"},               // keep tag (mega216)
	"drakkan/sftpgo":                 {Target: loongLocalRegistry + "sftpgo"},               // keep tag (mega216)
	"yisier1/npc":                    {Target: loongLocalRegistry + "npc"},                  // keep tag (mega216)
	"yisier1/nps":                    {Target: loongLocalRegistry + "nps"},                  // keep tag (mega216)
	"weishaw/sub2api":                {Target: loongLocalRegistry + "sub2api"},              // keep tag (mega216)
	"jokerwho/palworld-server-tool":  {Target: loongLocalRegistry + "palworld-server-tool"}, // keep tag (mega216)
	"gitea/act_runner":               {Target: loongLocalRegistry + "act-runner"},           // keep tag (mega216)
	"calciumion/new-api":             {Target: loongLocalRegistry + "new-api"},              // keep tag (mega216)
	"xhofe/alist":                    {Target: loongLocalRegistry + "alist"},                // keep tag (mega216)
	"keycloak/keycloak":              {Target: loongLocalRegistry + "keycloak"},             // keep tag (mega216)
	"metabase/metabase":              {Target: loongLocalRegistry + "metabase"},             // keep tag (mega216)
	"nacos/nacos-server":             {Target: loongLocalRegistry + "nacos"},                // keep tag (mega216)
	"apache/rocketmq":                {Target: loongLocalRegistry + "rocketmq"},             // keep tag (mega216)
	"bladex/sentinel-dashboard":      {Target: loongLocalRegistry + "sentinel-dashboard"},   // keep tag (mega216)
	"gotson/komga":                   {Target: loongLocalRegistry + "komga"},                // keep tag (mega216)
	"bitnamilegacy/kafka":            {Target: loongLocalRegistry + "kafka"},                // keep tag (mega216)
	"bitnamilegacy/tomcat":           {Target: loongLocalRegistry + "tomcat"},               // keep tag (mega216)
	"corentinth/it-tools":            {Target: loongLocalRegistry + "it-tools"},             // keep tag (mega216)
	"ghcr.io/gchq/cyberchef":         {Target: loongLocalRegistry + "cyberchef"},            // keep tag (mega216)
	"doocs/md":                       {Target: loongLocalRegistry + "wechatmarkdowneditor"}, // keep tag (mega216)
	"fogforest/yesplaymusic":         {Target: loongLocalRegistry + "yesplaymusic"},         // keep tag (mega216)
	"wcjiang/linux-command":          {Target: loongLocalRegistry + "linux-command"},        // keep tag (mega216)
	"adminer":                        {Target: loongLocalRegistry + "adminer"},              // keep tag (mega216)
	"ghcr.io/digininja/dvwa":         {Target: loongLocalRegistry + "dvwa"},                 // keep tag (mega216)
	"roundcube/roundcubemail":        {Target: loongLocalRegistry + "roundcube"},            // keep tag (mega216)
	"kodcloud/kodbox":                {Target: loongLocalRegistry + "kodbox"},               // keep tag (mega216)
	"bellamy/wallos":                 {Target: loongLocalRegistry + "wallos"},               // keep tag (mega216)
	"linuxserver/nextcloud":          {Target: loongLocalRegistry + "nextcloud"},            // keep tag (mega216)
	"star7th/showdoc":                {Target: loongLocalRegistry + "showdoc"},              // keep tag (mega216)
	"ddsderek/easyimage":             {Target: loongLocalRegistry + "easyimage2"},           // keep tag (mega216)
	"tencentci/discuz":               {Target: loongLocalRegistry + "discuz"},               // keep tag (mega216)
	"verdaccio/verdaccio":            {Target: loongLocalRegistry + "verdaccio"},            // keep tag (mega216)
	"mongo-express":                  {Target: loongLocalRegistry + "mongo-express"},        // keep tag (mega216)
	"rediscommander/redis-commander": {Target: loongLocalRegistry + "redis-commander"},      // keep tag (mega216)
	"nodered/node-red":               {Target: loongLocalRegistry + "node-red"},             // keep tag (mega216)
	"linuxserver/qbittorrent":        {Target: loongLocalRegistry + "qbittorrent"},          // keep tag (mega216)
	"valkey/valkey":                  {Target: loongLocalRegistry + "valkey"},               // keep tag (mega216)
	"zabbix/zabbix-agent":            {Target: loongLocalRegistry + "zabbix-agent"},         // keep tag (mega216)
	// --- wave29 (2026-11-11) ---
	"apache/answer":           {Target: loongLocalRegistry + "answer"},      // keep tag (wave29)
	"bytebase/bytebase":       {Target: loongLocalRegistry + "bytebase"},    // keep tag (wave29)
	"bytebase/dbhub":          {Target: loongLocalRegistry + "dbhub"},       // keep tag (wave29)
	"emlog/emlog":             {Target: loongLocalRegistry + "emlog"},       // keep tag (wave29)
	"hub.zentao.net/app/zentao": {Target: loongLocalRegistry + "zentao"},    // keep tag (wave29)
	"fireflyiii/core":         {Target: loongLocalRegistry + "firefly-iii"}, // keep tag (wave29)
	// --- wave30 (2026-11-11) ---
	"frooodle/s-pdf":        {Target: loongLocalRegistry + "stirling-pdf"}, // keep tag (wave30)
	"linuxserver/heimdall":  {Target: loongLocalRegistry + "heimdall"},     // keep tag (wave30)
	"lissy93/dashy":         {Target: loongLocalRegistry + "dashy"},        // keep tag (wave30)
	// --- wave31 (2026-11-11) ---
	"yidadaa/chatgpt-next-web": {Target: loongLocalRegistry + "chatgpt-next-web"}, // keep tag (wave31)
	"ghcr.io/gethomepage/homepage": {Target: loongLocalRegistry + "homepage"},    // keep tag (wave31)
	// --- wave32 (2026-11-12) ---
	"lissy93/web-check":      {Target: loongLocalRegistry + "web-check"},   // keep tag (wave32)
	"outlinewiki/outline":    {Target: loongLocalRegistry + "outline"},     // keep tag (wave32)
	// --- wave34 (2026-11-14) ---
	"binwiederhier/ntfy":     {Target: loongLocalRegistry + "ntfy"},        // keep tag (wave34)
	"ghcr.io/plankanban/planka": {Target: loongLocalRegistry + "planka"},   // keep tag (wave34)
	// --- wave36 (2026-11-14) ---
	"docmost/docmost":        {Target: loongLocalRegistry + "docmost"},     // keep tag (wave36)
	"triliumnext/trilium":    {Target: loongLocalRegistry + "trilium"},     // keep tag (wave36)
	// --- wave37 (2026-11-14) ---
	"ghcr.io/nodebb/nodebb":  {Target: loongLocalRegistry + "nodebb"},      // keep tag (wave37)
	// --- wave38 (2026-11-14) ---
	"moonrailgun/tailchat":   {Target: loongLocalRegistry + "tailchat"},    // keep tag (wave38)
	// --- wave39 (2026-11-15) ---
	"registry.cn-qingdao.aliyuncs.com/dataease/dataease": {Target: loongLocalRegistry + "dataease"}, // keep tag; amd64 fatjar repack onto temurin alpine-jre
	"1dev/server":            {Target: loongLocalRegistry + "onedev"},         // keep tag; amd64 product repack, native wrapper bypassed (pure-java io.onedev.wrapper)
	"code.forgejo.org/forgejo/runner": {Target: loongLocalRegistry + "forgejo-runner"}, // keep tag; goproxy cross build
	"docker":                 {Target: loongLCRRegistry + "library/docker", Fallback: "v28.3.0-dind"}, // forgejo-runner dind sidecar
	// --- wave42 (2026-11-15) ---
	"codeberg.org/forgejo/forgejo": {Target: loongLocalRegistry + "forgejo"}, // keep tag; PC cross build (webpack frontend + go loong64), gitea-compatible alpine layout
	// --- wave43 (2026-11-15) MED batch ---
	"jgraph/drawio":  {Target: loongLocalRegistry + "drawio"}, // keep tag; amd64 webapp extraction onto lcr tomcat 9-jdk11 (static war, arch-neutral)
	"diygod/rsshub":  {Target: loongLocalRegistry + "rsshub"}, // keep tag; device build (pure-JS node app, pnpm + npmmirror, no native deps)
	// --- wave44 (2026-11-15) ---
	"ghost": {Target: loongLocalRegistry + "ghost"}, // keep tag; device build: npm install + libvips 8.18.7 source build (sharp 0.35.5), node-gyp better-sqlite3, wasm fallback bindings
	// --- wave45 (2026-11-16) node24 family ---
	"n8nio/n8n":    {Target: loongLocalRegistry + "n8n"},    // keep tag; device build: npm i -g n8n on self-built node 24.22.3, node-gyp sqlite3 + isolated-vm
	"nocodb/nocodb": {Target: loongLocalRegistry + "nocodb"}, // keep tag; amd64 tree swap-in: sharp 0.33.4 src build + sqlite3/lz4 npm-pkg rebuild (lz4 skipped: nan vs node24 V8)
	"greper/certd":  {Target: loongLocalRegistry + "certd"},  // keep tag; amd64 tree swap-in: better-sqlite3 11.10.0 node-gyp, pnpm symlinks rebuilt
	// --- wave46 (2026-11-16) ---
	"ghcr.io/ourongxing/newsnow": {Target: loongLocalRegistry + "newsnow"}, // keep tag; ghcr Nitro standalone tree swap-in on slim certd base, better-sqlite3 12.6.2 compiled in ghost container
	"koishijs/koishi":            {Target: loongLocalRegistry + "koishi"},  // keep tag; alpine boilerplate zip -> tar convert (symlinks+modes), cp-in entrypoint, npm start on slim certd base
	// --- wave47 (2026-11-16) ---
	"henryclw/jsonhero-web": {Target: loongLocalRegistry + "jsonhero"}, // keep tag; remix prod rebuild on node24 (esbuild 0.14.51 native), ssr-shim --require (navigator/document/sessionStorage), setup node
	"kingwrcy/mblog":        {Target: loongLocalRegistry + "mblog"},    // keep tag; temurin17-trixie base + jar swap-in + host-gcc xerial sqlite native libsqlitejdbc.so (first Java app)
	// --- wave48 (2026-11-16) ---
	"certimate/certimate": {Target: loongLocalRegistry + "certimate"}, // keep tag; alpine3.22 base + go1.25.14 cross-compiled static binary (ui built on PC node24, embed)
	// --- wave49 (2026-11-16) ---
	"chigusa/bililive-go": {Target: loongLocalRegistry + "bililive-go"}, // keep tag; debian sid base + apt ffmpeg (deb.debian.org main ships loong64) + go1.25.14 cross-compiled -tags release binary (CRA frontend built on PC node24, go:embed)
	"b3log/siyuan":        {Target: loongLocalRegistry + "siyuan"},      // keep tag; alpine3.22 base + kernel built natively on device (CGO sqlcipher+fts5, GOTOOLCHAIN=go1.26.10) + frontend artifacts from PC pnpm12 webpack; OCR stripped (no loong64 onnxruntime, runtime dlopen -> ErrUnavailable fallback)
	// --- wave50 (2026-11-16) ---
	"docker.cnb.cool/mliev/dwz/dwz-server": {Target: loongLocalRegistry + "mliev-dwz"}, // keep tag; go1.26.8 CGO_ENABLED=0 cross-compile (Gin+GORM, embed static/templates/migrations) + vben admin dist built on PC node24 pnpm10.34.5; alpine3.22 runtime, CMD ["./dwz","start"]
	"redpandadata/console":                {Target: loongLocalRegistry + "redpanda-console"}, // keep tag; go1.26.10 CGO_ENABLED=0 cross-compile backend/cmd/api + rsbuild frontend built on PC bun1.4.3 (frontend/build -> backend/pkg/embed/frontend); alpine3.22 runtime
	// --- wave51 (2026-11-16) ---
	"svhd/logto": {Target: loongLocalRegistry + "logto"}, // keep tag; PC node24 pnpm10 monorepo build + device prod install (node26-debian, native rebuild via apt toolchain); ENTRYPOINT ["npm","run"] CMD ["start"]
	"ghcr.io/umami-software/umami": {Target: loongLocalRegistry + "umami"}, // keep tag (latest 3.4.1; 2.20.2 ${PANEL_DB_TYPE} ref unmappable); prisma7 engineType=client pure-JS adapter-pg (no native engine); SKIP_DB_MIGRATION + SQL-file entrypoint; PC pnpm12.3.4 next build standalone on node22-debian runtime
	// --- wave52 (2026-11-16) ---
	"keking/kkfileview":  {Target: loongLocalRegistry + "kkfileview"}, // keep tag; maven-built server (device temurin8+jdk aliyun mirror) on debian trixie base with apt libreoffice/cjk fonts/ffmpeg (deb.debian.org loong64); java -jar entrypoint
	"sonatype/nexus3":   {Target: loongLocalRegistry + "nexus"}, // keep tag; arm64 image layer extraction (pure-java Karaf app) on temurin21-trixie; sed vmoptions to /nexus-data, Xmx1024m
	// --- wave53 (2026-11-16) ---
	"sonarqube": {Target: loongLocalRegistry + "sonarqube"}, // keep tag; arm64 image layer extraction (bundled ES9 pure-java, JNA/libvec native load fails -> WARN fallback, ES binds 127.0.0.1 loopback local mode) on temurin25-alpine-jre; ENTRYPOINT java -jar lib/sonarqube.jar (official entrypoint.sh needs bash)
	// --- wave54 (2026-11-17) ---
	"bitnamilegacy/mysql": {Target: loongLCRRegistry + "library/mysql", Fallback: "8.4.3", ByMajor: map[string]string{"5": "5.7.29-an23", "8": "8.4.3"}}, // wave54 gap triage: mysql-cluster app was unmapped (no_rule); same lcr mysql as plain mysql rule
	// --- wave55 (2026-11-17) ---
	"1panel/kubepi": {Target: loongLocalRegistry + "kubepi"}, // keep tag; PC node24 builds 3 vue/angular frontends embed into kubepi-server (go1.26 cross) + gotty cross (pty upgraded to v1.1.24 for loong64); alpine3.22 base with kubectl(loongnix deb)/helm/k9s/fzf cross-built
	"mattermost/mattermost-team-edition": {Target: loongLocalRegistry + "mattermost"}, // keep tag; official amd64 layer extraction (webapp client/plugins/i18n) + server binary go1.26.10 cross via go.work(server/public member); alpine3.22 static
	// --- wave56 (2026-11-17) ---
	"getmeili/meilisearch": {Target: loongLocalRegistry + "meilisearch"}, // keep tag; first Rust batch: on-device native build (lcr rust:1.100 image, rust-toolchain.toml 1.98.1 auto-synced by rustup loong64 channel, rsproxy fetched 800 crates); debian:sid+tini base (compose command "sh -c" overrides CMD so ENTRYPOINT must be ["/bin/tini","--"] like official)
	// --- wave57 (2026-11-17) Rust batch #2 ---
	"rustfs/rustfs": {Target: loongLocalRegistry + "rustfs"}, // keep tag; on-device native build (lcr rust:1.100); 2 git deps (rustfs/uring, s3s-project/s3s) vendored at locked revs as path deps - cargo libgit2 hangs on github from CN; LINKER: 497MB no-LTO binary overflows ld.bfd R_LARCH_B26 +-128MB (aws-lc bcm.o then mimalloc static.o - whack-a-mole, -fno-plt only fixes PLT calls not intra-lib branches) -> MUST use lld via /usr/local/bin/cc wrapper `exec /usr/bin/cc -fuse-ld=lld "$@"` (rustc linker="cc" PATH-resolved; wrapper keeps RUSTFLAGS/cache intact, full rebuild avoided; lld thunks B26 automatically and accepts rustc's -Wl,--no-rosegment); CARGO_PROFILE_RELEASE_LTO=off (plugin-lto aborts at lib->bin on loongarch); console UI = rust-embed of rustfs/static/ AT COMPILE TIME, git ships only .gitkeep -> unzip https://dl.rustfs.com/artifacts/console/rustfs-console-latest.zip into static/ (device can reach dl.rustfs.com directly) + touch rustfs/src/admin/console.rs to force bin recompile (cargo does not hash that dir); console serves on :9001 at SUBPATH /rustfs/console/index.html (/ and /login fall through to the S3 handler which parses them as bucket names -> 403); smoke probes: S3 /minio/health/live=200, console subpath=200 (S3 root 403 = normal unauth); debian:sid base (glibc, NOT alpine) + jq curl ca-certificates, official-style entrypoint.sh+bootstrap, RUSTFS_VOLUMES=/data, USER root (bind-mount ./data is host-root-owned; official USER rustfs uid10001 cannot write bind mounts)
	"stalwartlabs/stalwart": {Target: loongLocalRegistry + "stalwart-mail"}, // keep tag; on-device native build (lcr rust:1.100) with --no-default-features --features sqlite,postgres,mysql,s3,redis,enterprise (rocks dropped: vendored rust-rocksdb lacks the C++ git submodule a codeload tarball cannot carry); [patch.crates-io] git deps (hickory-dns) vendored as path deps; aws-lc-sys hard dep (main.rs installs provider) built via cmake/clang bindgen; CONFIG_PATH=/opt/stalwart/config.json so 1panel ./data:/opt/stalwart mount holds config+store (upstream image uses /etc/stalwart which the 1panel compose does not persist)
	"timberio/vector": {Target: loongLocalRegistry + "vector"}, // keep tag; on-device native build (lcr rust:1.100) with --no-default-features --features target-base,tikv-jemallocator (vendored DROPPED: rdkafka gssapi-vendored pulls krb5-src 1.19.2 whose K&R C hard-errors on sid GCC15; openssl stays vendored via unconditional target-dep so no system libssl needed); 3 loong64 patches: [patch.crates-io] nix->path vendored 0.23.2 with #![allow(missing_abi,unexpected_cfgs)] inner attrs (heim pins nix 0.23; PATH deps lose cargo --cap-lints so rustc 1.100 deny-default lints fire - [lints] table did NOT apply); heim fork (update-deps) checked out to tree, 9 heim-* crates path-patched, every lib.rs deny(->allow sed (heim-cpu/disk/net dead_code+deprecated+cfg-musl gaps; ut_session i64->i32 cast on loongarch64); databricks-zerobus-ingest-sdk path-vendored with build.rs PROTOC hardcoded /usr/bin/protoc (protoc-bin-vendored has no loong64 asset; apt protobuf-compiler in build container); debian:sid base, ENTRYPOINT ["/usr/bin/vector"]
	// --- wave58 (2026-11-19) mixed batch ---
	"rustdesk/rustdesk-server-s6": {Target: loongLocalRegistry + "rustdesk"}, // keep tag; on-device native build (lcr rust:1.100 + rsproxy; source=codeload 1.1.16 + hbb_common submodule @83419b6 fetched separately - codeload tarballs carry no submodule content; apt protobuf-compiler for hbb_common protos; TUNA-ize apt or lld-less deb.debian.org crawls for 20min+); s6-overlay has NO loong64 binaries -> entrypoint.sh runs BOTH hbbs (-r $RELAY) and hbbr in one container + trap/wait, WORKDIR /data VOLUME /data, ports 21115-21119; small binary, plain bfd link fine (lld cc-wrapper only needed for >128MB rustfs-class links)
	"apacherocketmq/rocketmq-dashboard": {Target: loongLocalRegistry + "rocketmq-dashboard"}, // keep tag; jar is arch-independent: w43_pull.py extracted rocketmq-dashboard.jar (134MB, Build-Jdk 17) from amd64 image layers on PC; base=loong/java:21 (self-built Loongnix JIT JDK21, w53 asset) runs it fine; compose has custom entrypoint (users.properties gen + exec java -jar) so image needs ONLY java in PATH + /rocketmq-dashboard.jar; cold start ~2min on 2K3000 (Spring fat-jar, 71s context init) - smoke poll must wait >90s
	"whyour/qinglong": {Target: loongLocalRegistry + "qinglong"}, // keep tag; split build: static/ (front max build -> static/dist + back tsc -> static/build/app.js; CI-only artifact not in repo) built on PC with pnpm@8.3.1 (lockfile pin) + NODE_OPTIONS preload shim for process.binding('http_parser') -> http-parser-js (umi 4.4.4 plugins esmi/tmpFiles die on node>=22 without it; esmi plugin also stubbed {__esModule:true,default:noop}); node_modules (--prod, native sqlite3 etc.) built on device in lcr node:22.17.0-trixie; runtime image FROM node:22.17.0-trixie + trixie python3.13 (official uses python:3.11 image; PYTHON_SHORT_VERSION=3.13), USER root (1panel bind-mount), ecosystem runs static/build/app.js via pm2; 2.23.0-debian tag aliased from 2.24.0 build (project precedent: version tags share one build)
	// --- wave59 (2026-11-20) mixed batch ---
	"dbeaver/cloudbeaver": {Target: loongLocalRegistry + "cloudbeaver"}, // keep tag; app layers (equinox jars + web assets + scripts, all arch-independent) extracted from amd64 image via w43_pull (3 /opt/cloudbeaver layers; skip temurin layers); base=loong/java:21; launch-product.sh does su dbeaver -c run-cloudbeaver-server.sh which execs PATH java with --add-opens (fine on 21); useradd dbeaver uid/gid 8978, EXPOSE 8978, VOLUME /opt/cloudbeaver/workspace; Jetty starts ~10s, smoke 200 on /
	"pgsty/silo": {Target: loongLocalRegistry + "silo"}, // keep tag; PC cross-compile (go1.27.3 GOOS=linux GOARCH=loong64 CGO_ENABLED=0, goproxy.cn, ~1min, 96MB binary; ldflags -X github.com/minio/minio.Version=<RELEASE tag>); debian:sid-slim + ca-certificates curl; ENTRYPOINT silo + CMD server /data --console-address :9001 --address :9000 (compose supplies same command); MinIO-compatible: /minio/health/live=200 + console :9001=200, default creds minioadmin:minioadmin warn in logs
	// --- databases / queues ---
	"redis":     {Target: loongLCRRegistry + "library/redis", Fallback: "7.4.1", ByMajor: map[string]string{"5": "5.0.14", "6": "6.2.13", "7": "7.4.1", "8": "7.4.1"}},
	"bitnamilegacy/redis": {Target: loongLCRRegistry + "library/redis", Fallback: "7.4.1", ByMajor: map[string]string{"5": "5.0.14", "6": "6.2.13", "7": "7.4.1", "8": "7.4.1"}}, // wave42 redis-cluster
	"mysql":     {Target: loongLCRRegistry + "library/mysql", Fallback: "8.4.3", ByMajor: map[string]string{"5": "5.7.29-an23", "8": "8.4.3"}},
	"mariadb":   {Target: loongLCRRegistry + "library/mariadb", Fallback: "13.1.1", ByMajor: map[string]string{"10": "10.5.18", "12": "13.1.1", "13": "13.1.1"}},
	"postgres":  {Target: loongLCRRegistry + "library/postgres", Fallback: "15.7-sid", ByMajor: map[string]string{"13": "13.19-alpine3.21", "14": "13.19-alpine3.21", "15": "15.7-sid", "16": "15.7-sid", "17": "17-sid", "18": "17-sid"}},
	"bitnamilegacy/postgresql": {Target: loongLCRRegistry + "library/postgres", Fallback: "15.7-sid", ByMajor: map[string]string{"13": "13.19-alpine3.21", "14": "13.19-alpine3.21", "15": "15.7-sid", "16": "15.7-sid", "17": "17-sid", "18": "17-sid"}}, // wave42 pg-cluster
	"mongo":     {Target: loongLCRRegistry + "library/mongo", Fallback: "6.0.26"},
	"memcached": {Target: loongLCRRegistry + "library/memcached", Fallback: "1.6.21-alpine"},
	"rabbitmq":  {Target: loongLCRRegistry + "library/rabbitmq", Fallback: "4.1.2-management"},
	"emqx/emqx": {Target: loongLCRRegistry + "emqx/emqx", Fallback: "6.3.2"},
	"nats":      {Target: loongLCRRegistry + "library/nats", Fallback: "2.10.24-alpine"},

	// --- search / observability ---
	"elasticsearch": {Target: loongLCRRegistry + "library/elasticsearch", Fallback: "9.5.5", ByMajor: map[string]string{"9": "9.5.5"}},
	"kibana":        {Target: loongLCRRegistry + "library/kibana", Fallback: "9.4.2", ByMajor: map[string]string{"9": "9.4.2"}},
	"logstash":      {Target: loongLCRRegistry + "library/logstash", Fallback: "9.5.5", ByMajor: map[string]string{"9": "9.5.5"}},
	"influxdb":      {Target: loongLCRRegistry + "library/influxdb", Fallback: "3.9.6-alpine"},

	// --- web / cms ---
	"phpmyadmin":        {Target: loongLCRRegistry + "library/phpmyadmin", Fallback: "5.2.3"},
	"wordpress":         {Target: loongLCRRegistry + "library/wordpress", Fallback: "7.0.2-php8.5-fpm-alpine", ByMajor: map[string]string{"7": "7.0.2-php8.5-fpm-alpine"}},
	"yourls":            {Target: loongLCRRegistry + "library/yourls", Fallback: "1.10.6"},
	"crazymax/flarum":   {Target: loongLCRRegistry + "library/flarum", Fallback: "latest"},
	"postgis/postgis":   {Target: loongLCRRegistry + "postgis/postgis", Fallback: "16-3.4"},
	"pgvector/pgvector": {Target: loongLCRRegistry + "library/pgvector", Fallback: "0.8.0"},

	// --- ops / ci ---
	"registry":                 {Target: loongLCRRegistry + "library/registry", Fallback: "3.1.1", ByMajor: map[string]string{"3": "3.1.1"}},
	"jenkins/jenkins":          {Target: loongLCRRegistry + "jenkins/jenkins", Fallback: "2.583-jdk21"},
	"gitlab/gitlab-ce":         {Target: loongLCRRegistry + "gitlab/gitlab-ce", Fallback: "17.6.2"},
	"grafana/grafana":          {Target: loongLCRRegistry + "grafana/grafana", Fallback: "12.0.8", ByMajor: map[string]string{"13": "13.0.2-alpine", "9": "9.5.2"}},
	"jaegertracing/all-in-one": {Target: loongLCRRegistry + "jaegertracing/all-in-one", Fallback: "1.6.0"},
	"hashicorp/consul":         {Target: loongLCRRegistry + "library/consul", Fallback: "1.15.2"},
	"portainer/portainer-ce":   {Target: loongLCRRegistry + "portainer/portainer-ce", Fallback: "2.11.1"},

	// --- misc ---
	"neo4j":         {Target: loongLCRRegistry + "library/neo4j", Fallback: "5.26.32"},
	"2fauth/2fauth": {Target: loongLCRRegistry + "library/2fauth", Fallback: "latest"},
}

// basename fallback rules for base images referenced by any compose file
var loong64BaseRules = map[string]loong64Rule{
	"alpine":  {Target: loongLCRRegistry + "library/alpine", Fallback: "3.24"},
	"busybox": {Target: loongLCRRegistry + "library/busybox", Fallback: "latest"},
	"python":  {Target: loongLCRRegistry + "library/python", Fallback: "3.11.7"},
	"node":    {Target: loongLCRRegistry + "library/node", Fallback: "v20.19.3-debian"},
}

// repositories confirmed unportable; installs are rejected with a clear message
var loong64BlockedRepos = map[string]bool{
	"jellyfin/jellyfin": true, // .NET runtime has no official loong64 port
	// mega216 evaluation batch (2026-11-07), reasons per app
	"ghcr.io/666ghj/bettafish":            true, // Weibo-sentiment AI stack, heavy deps, no loong64 GPU support
	"linuxserver/code-server":             true, // VS Code build chain too heavy for loong64
	"1panel/comfyui":                      true, // requires torch, no official loong64 wheels
	"harness/harness":                     true, // closed-source CI platform, heavy stack
	"ghcr.io/ggml-org/llama.cpp":          true, // LLM inference impractical on 8G RAM CPU-only device
	"localai/localai":                     true, // x86-optimized llama.cpp backends, no practical loong64 use
	"mcr.microsoft.com/mssql/server":      true, // SQL Server has no loong64 distribution
	"jc21/nginx-proxy-manager":            true, // build chain too heavy; use built-in website module
	"ollama/ollama":                       true, // GPU/CUDA stack, no GPU here; CPU mode useless on 8G RAM
	"ghcr.io/open-webui/open-webui":       true, // depends on torch, no official loong64 wheels
	"openvpn/openvpn-as":                  true, // Access Server ships closed amd64/arm64 binaries only
	"thijsvanloef/palworld-server-docker": true, // steamcmd+wine x86 game server, meaningless on loong64
	"lmsysorg/sglang":                     true, // GPU inference framework, no loong64 build
	"teamspeak":                           true, // closed-source x86 binary
	"ghcr.io/tiny-craft/tiny-rdm":         true, // desktop GUI app wrapper, not fit for NAS use
	"shuzhiyinhang/vexdb":                 true, // bank closed-source vector DB, no public loong64 channel
	"dockurr/windows":                     true, // QEMU x86 guest needs KVM, no x86 virt on loong64
	"zhaojun1998/zfile":                   true, // ZFile 5.x closed source, x86/arm binaries only
	// wave40 (2026-11-15) VAR-batch triage
	"mcr.microsoft.com/dotnet/aspnet": true, // Microsoft ships no loong64 .NET runtime
	"eqalpha/keydb":                   true, // C++ Redis fork, no loong64 build; use lcr redis instead
	// wave42 (2026-11-15): upstream dead - GitHub repo 404, Hub repo gone (daocloud: invalid repository)
	"firezmc/dosgame-web-docker": true,
	// wave43 (2026-11-15): Bun-compiled single binary (/app/convertx); Bun runtime has no loong64 port
	"c4illin/convertx": true,
	// wave 14 audit (2026-11-08): F verdict leftover rule removed from mapping table
	// wave58: rustdesk/rustdesk-server-s6 block REMOVED - 1.1.16 build landed (loong/rustdesk:1.1.16,
	// store repack + E2E green); the stale block shadowed the live mapping rule since
	// isBlockedLoong64Image is checked BEFORE mapLoong64Image
	"blinkospace/blinko": true, // wave59: Bun runtime has no loong64 port AND Prisma query engines have no loong64 builds (convertx-class double blocker)
	// --- wave54 (2026-11-17) gap triage: audit14 no_rule sweep, 34 high-confidence unportables ---
	// LLM/AI stack (python+GPU/torch deps, no loong64 wheels; 1Panel-official images ship amd64/arm64 only)
	"rockchin/langbot":                 true, // LLM bot framework, python native deps
	"langflowai/langflow":              true, // LangFlow, heavy python AI stack
	"soulter/astrbot":                  true, // LLM bot, python native deps
	"lss233/kirara-agent-framework":    true, // LLM agent framework
	"agentscope/qwenpaw":               true, // LLM agent (alibaba qwen stack)
	"1panel/hermes-agent":              true, // 1Panel AI agent, official image amd64/arm64 only
	"1panel/clawswarm":                 true, // AI swarm orchestration, official image amd64/arm64 only
	"1panel/ai-gateway":                true, // LLM gateway, official image amd64/arm64 only
	"1panel/ai-portal":                 true, // AI portal, official image amd64/arm64 only
	"1panel/deepseek-harness":          true, // DeepSeek eval harness
	"1panel/maxkb":                     true, // AI knowledge base (pgvector+LLM), official image amd64/arm64 only
	"dataease/sqlbot":                  true, // AI SQL bot (dataease), LLM deps
	"ghcr.io/tencentcloud/octop":       true, // tencent cloud AI ops, closed stack
	"1panel/openclaw":                  true, // AI agent runtime, official image amd64/arm64 only
	// heavy C++ databases / search engines with no loong64 build and >8G RAM appetite
	"clickhouse/clickhouse-server": true,   // C++ engine, no official loong64 build
	"oceanbase/oceanbase-ce":       true,   // no loong64 community build; needs >>8G RAM
	"starrocks/allin1-ubuntu":      true,   // no loong64 build; allin1 needs >>8G RAM
	"manticoresearch/manticore":    true,   // C++ engine, no loong64 build
	"typesense/typesense":          true,   // C++ engine, no loong64 build
	// zabbix (C server + php agent stack, no loong64 images; xinchuang edition shares repo)
	"zabbix/zabbix-proxy-mysql":  true,
	"zabbix/zabbix-server-mysql": true,
	// python mega-stacks / chromium-embedded (native wheels & chromium lack loong64)
	"homeassistant/home-assistant":      true, // hundreds of native wheels, no loong64 wheel chain
	"jupyter/scipy-notebook":            true, // scipy stack wheels unavailable for loong64
	"archivebox/archivebox":             true, // core extraction needs chromium, no loong64
	"dgtlmoon/changedetection.io":      true, // playwright chromium core, no loong64
	"ghcr.io/browserless/chrome":        true, // is a chromium, no loong64 build
	// heavyweight platforms (build chain or resource infeasible on this device)
	"bitnamilegacy/discourse":            true, // ruby+sidekiq mega-stack, no loong64 images
	"ghcr.io/huginn/huginn":              true, // ruby agent platform, no loong64 base
	"jumpserver/jms_all":                 true, // all-in-one mega image, no loong64 build
	"metersphere/metersphere-ce-allinone": true, // 10GB all-in-one, resource infeasible
	"xiaozhu674/gameservermanager":       true, // game server management (steamcmd/wine x86)
	"onlyoffice/documentserver":          true, // C++/node doc server, huge build chain, no loong64
	"litespeedtech/openlitespeed":        true, // C++ server + lsphp, no loong64 build
	"ghcr.io/yetone/magpie":              true, // desktop GUI tool, not NAS-shaped
}

// mapImagesForLoong64 rewrites service images in a parsed docker-compose map.
// It returns an error when a service references an image known to be
// unportable, so the caller can abort the install with a clear message.
func mapImagesForLoong64(composeMap map[string]interface{}) error {
	if runtime.GOARCH != "loong64" || composeMap == nil {
		return nil
	}
	servicesValue, ok := composeMap["services"]
	if !ok || servicesValue == nil {
		return nil
	}
	services, ok := servicesValue.(map[string]interface{})
	if !ok {
		return nil
	}
	// note: services MAY also carry a "build" section (e.g. openresty ships a
	// build context for dynamic modules alongside its published image); the
	// rule table below is an explicit allowlist, so mapping stays safe — the
	// rewritten image is present in the local registry and compose never
	// needs to invoke the build.
	for name, svcValue := range services {
		svc, ok := svcValue.(map[string]interface{})
		if !ok {
			continue
		}
		raw, ok := svc["image"].(string)
		if !ok {
			continue
		}
		image := strings.Trim(strings.TrimSpace(raw), `"'`)
		if isBlockedLoong64Image(image) {
			return errors.New(loong64UnsupportedMsg)
		}
		mapped, ok := mapLoong64Image(image)
		if !ok {
			continue
		}
		svc["image"] = mapped
		global.LOG.Infof("[loong64] app service %q image %s -> %s", name, image, mapped)
	}
	return nil
}

func isBlockedLoong64Image(image string) bool {
	repo, _ := splitImageRef(image)
	return loong64BlockedRepos[repo]
}

func mapLoong64Image(image string) (string, bool) {
	if image == "" || strings.Contains(image, "${") || strings.Contains(image, "loongnix") ||
		strings.HasPrefix(image, "127.0.0.1:") || strings.Contains(image, "@") {
		return "", false
	}
	repo, tag := splitImageRef(image)
	if rule, ok := loong64RepoRules[repo]; ok {
		return renderLoong64Rule(rule, tag), true
	}
	base := repo[strings.LastIndex(repo, "/")+1:]
	if rule, ok := loong64BaseRules[base]; ok {
		return renderLoong64Rule(rule, tag), true
	}
	return "", false
}

func renderLoong64Rule(rule loong64Rule, wantTag string) string {
	if rule.Fallback == "" {
		// keep requested tag
		return joinImageTag(rule.Target, wantTag)
	}
	if rule.ByMajor != nil && wantTag != "" {
		major := wantTag
		if dot := strings.IndexAny(wantTag, ".-"); dot > 0 {
			major = wantTag[:dot]
		}
		if byMajor, ok := rule.ByMajor[major]; ok {
			return joinImageTag(rule.Target, byMajor)
		}
	}
	return joinImageTag(rule.Target, rule.Fallback)
}

func splitImageRef(image string) (repo, tag string) {
	image = strings.Trim(strings.TrimSpace(image), `"'`)
	repo, tag = image, ""
	if idx := strings.LastIndex(image, ":"); idx > strings.LastIndex(image, "/") {
		repo, tag = image[:idx], image[idx+1:]
	}
	return repo, tag
}

func joinImageTag(repo, tag string) string {
	if tag == "" {
		return repo
	}
	return repo + ":" + tag
}
