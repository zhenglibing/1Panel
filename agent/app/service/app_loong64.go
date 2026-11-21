package service

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/1Panel-dev/1Panel/agent/global"
)

// On linux/loong64 hosts most Docker Hub images cannot be pulled because
// they ship no loong64 manifest. This layer rewrites appstore image
// references to loong64-capable equivalents:
//   - lcr.loongnix.cn (Loongson community registry, which rebuilds popular
//     images for loong64)
//   - a local registry (127.0.0.1:5000/loong/...) for images we build on
//     the device (e.g. OpenResty)
//   - a public ACR mirror of that local registry as a secondary source:
//     when the local registry does not have a mapped repo:tag (or is down),
//     the ref is rewritten to the ACR mirror so installs keep working
//     online (wave66b, 2026-11-21; namespace mirrors all self-built
//     images, digests verified tag-by-tag)
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
	loongRepoNamespace    = "loong/" // registry repo path under the host part
	loongACRRegistry      = "crpi-a02ej0ppuvq7b0mw.cn-hangzhou.personal.cr.aliyuncs.com/1panel-loong64/"
	loong64UnsupportedMsg = "该应用暂无 LoongArch64（龙芯）版本镜像，已列入移植矩阵不支持清单；可在「容器」页用自定义 compose 部署替代镜像"
)

// probe base for localRegistryHas; package var so tests can point it at
// an httptest stub
var loongLocalProbeBase = "http://127.0.0.1:5000"

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
	// --- wave60 (2026-11-20) mixed batch ---
	"jc21/nginx-proxy-manager": {Target: loongLocalRegistry + "nginx-proxy-manager"}, // keep tag; s6-overlay has no loong64 -> custom entrypoint replicating prepare chain (usergroup/paths/dirs) + nginx (conf has own daemon off, DO NOT pass -g; needs include /etc/nginx/modules-enabled/*.conf for debian libnginx-mod-stream) + node backend index.js (port 3000); base lcr node:22.17.0-trixie + apt nginx/libnginx-mod-stream/certbot (SJTU main sid); PC builds frontend (yarn locale-compile FIRST - lang/*.json are formatjs-generated, then tsc+vite); backend yarn install --ignore-engines (@apidevtools wants node>=22.19, image has 22.17) + node-gyp@10 via npm_config_node_gyp (bcrypt/better-sqlite3/sqlite3 natives); entrypoint pre-creates nginx temp dirs (/tmp/nginx/body, /var/lib/nginx/cache/{public,private}); production.conf rendered from template with NPM_PEER_SECRET exported to backend; admin UI :81, smoke 200 after ~30s
	"dpage/pgadmin4": {Target: loongLocalRegistry + "pgadmin4"}, // keep tag; PyPI wheel install path (JS prebundled in wheel - skip the yarn/app-builder monorepo hell): loong/python:3.14.8 + venv with ALL native deps source-built on device (apt build-essential libpq-dev libffi-dev libssl-dev libkrb5-dev libjpeg-dev zlib1g-dev libbrotli-dev python3.14-dev cargo rustc; cffi needs python3.14-dev headers, gssapi needs libkrb5-dev, pillow needs jpeg/zlib, brotli MUST bump to 1.1.0 - 1.0.9 C code hits GCC15 'model' attribute error on loongarch); psycopg[binary]->psycopg (binary dist has no loong64 and never will) = pure-python protocol via ctypes libpq -> runtime needs libpq5; requirements installed from source tree w/ --no-deps wheel on top; venv persisted on /mnt across build attempts; first-boot migration prompts email/password -> entrypoint exports PGADMIN_SETUP_EMAIL/PASSWORD (SETUP vars are what migration reads, DEFAULT_* are docker-entrypoint-translated); DEFAULT_SERVER_PORT=80 to match store compose, smoke 302 login redirect
	// --- wave61 (2026-11-20) Go identity platform ---
	"ghcr.io/zitadel/zitadel": {Target: loongLocalRegistry + "zitadel"}, // keep tag; PC full-source build: buf generate (9 plugins: go/go-grpc/grpc-gateway/openapiv2/envoyproxy-validate@v1.3.3 + repo-local authoption/zitadel; BSR remote plugins unreachable from CN - all local, protoc 3.20.3 provides builtin js gen for console, grpc-web exe from releases) -> cp .artifacts to pkg/grpc -> 4x go generate statik (i18n/login-static/notifications/migrations - rakyll/statik CLI; MISSING these = runtime panic internal/i18n/fs.go) -> console Angular build (packages/zitadel-proto protoc-gen-es FIRST + packages/zitadel-client tsup FIRST + 5 strict-null source patches app-detail/memberships-table; lang stubs via console buf generate) -> console dist cp to internal/api/ui/console/static (go:embed) -> CGO_ENABLED=0 GOARCH=loong64 build = 173MB binary with embedded console; v4.19 env quirks: ZITADEL_MASTERKEY only binds via --masterkeyFromEnv flag (store compose has it; bare env = silently ignored!), per-field DB envs half-broken - use ZITADEL_DATABASE_POSTGRES_DSN (works), TLS needs --tlsMode disabled flag (ZITADEL_TLS_ENABLED env also ignored) -> image entrypoint wrapper auto-appends --tlsMode disabled when no tls flag present (reverse-proxy norm); smoke: pg companion + start-from-init --masterkey <32-char-exact> --tlsMode disabled -> healthz 200 + /ui/console/ 200
	// --- wave62 (2026-11-20) CRM monorepo, full device build ---
	"twentycrm/twenty": {Target: loongLocalRegistry + "twenty"}, // keep tag; v2.58.0 full device build (node:26.13.0-debian, yarn4 berry). Front vite7: lingui macros unbuildable on loong (enforce:pre resolveId throws) -> own enforce:pre resolveId aliases @lingui/{core,react}/macro to src/macro-shim.ts (t/msg/plural/select/selectOrdinal/Trans/Plural/Select/SelectOrdinal/i18n/useLingui passthrough); monaco-graphql worker ships iife (rollup refuses iife in code-split builds) -> GraphqlWorker stub class in setupMonacoEnvironment.ts; twenty-front-component-renderer has no dist in repo (build needs vite+generated sandbox doc that itself hits the worker bug) -> stub dist/index.{mjs,cjs,d.ts}; NODE_OPTIONS=--max-old-space-size=8192 (4GB default OOM at ~22k modules, device has 29GB). Server MUST be full tsc -p tsconfig.build.json --noEmitOnError false (SWC/transpile emits `typeof X==="function"?X:Object` guard for enum design:type -> @nestjs/graphql UndefinedTypeError; plain tsc emits String for string enums ONLY when types resolve -> MUST fix tsconfig.build.json paths "src/*":["./src/*"] (upstream maps to repo-root path, broken with baseUrl=".") AND rm tsconfig.build.tsbuildinfo (stale incremental skips re-emit)); runtime: require("src/...") alias kept verbatim by tsc -> symlink /app/node_modules/src -> /app/packages/twenty-server/dist; @lingui/core/macro real pkg forwards to build-time-only babel-plugin-lingui-macro (absent in prod focus) -> overwrite its index.js with CJS passthrough shim; bcrypt has no loong64 prebuild + enableScripts:false -> bcrypt.js shim re-exporting bcryptjs 2.4.3 (promise API compatible); prod node_modules via yarn workspaces focus --production (offline from .yarn/cache, 1005MB); entrypoint psql gate skips database:init:prod if schema core exists (stale-empty-schema trap after failed first run -> recreate DB); worker: DISABLE_DB_MIGRATIONS/CRON_JOBS_REGISTRATION=true (server does them); healthcheck retries>=60 (loong Nest bootstrap ~2min); smoke: fresh pg17+redis7.4 -> 615-step upgrade 0 fail, /healthz 200, /graphql 200, SPA 200
	// --- wave63 (2026-11-21) game panel + bookmark manager ---
	"githubyumao/mcsmanager-daemon": {Target: loongLocalRegistry + "mcsmanager-daemon"}, // keep tag (store pins latest; also tagged 4.18.3); PC node24 webpack BUNDLE=1 build (self-contained app.js, gitee clone - github flaky from CN) -> production-code; loong64 awareness upstream: official PTY release HAS pty_linux_loong64 (static Go); file_zip = MCSManager/Zip-Tools pure-Go -> GOARCH=loong64 cross 30s; 7z = debian sid `7zip` pkg 26.06 loong64 -> lib/7z_linux_loong64 shell wrapper exec /usr/bin/7zz (daemon `i` probe passes, 7z ops work); daemon base = loong/java:21 (Loongnix JDK21.0.13 for MC instances) + node /usr/local copied from lcr node:26.13.0-debian; dockerode via mounted /var/run/docker.sock spawns sibling game containers (user images their own concern); smoke: daemon 24444 200 + version log, web 23333 200
	"githubyumao/mcsmanager-web":    {Target: loongLocalRegistry + "mcsmanager-web"},    // keep tag (latest + 10.18.3); same PC build, panel webpack + vite frontend dist -> production-code/web on lcr node:26.13.0-debian; "local daemon config unreadable" WARN normal (daemon is separate container, connect via UI)
	"sissbruecker/linkding": {Target: loongLocalRegistry + "linkding"}, // keep tag (1.47.0-plus); device multi-stage: lcr node npm ci+vite static -> loong/python:3.14.8 venv via plain pip -r requirements (PC `uv export --no-dev --group postgres` from uv.lock - uv itself has NO loong64 wheel, pip sdist would rust-build for hours); cryptography 50 = maturin rust build with cargo+rustup copied from lcr rust:1.100 (CARGO_HOME/RUSTUP_HOME/PATH env); uwsgi/psycopg[c]/cffi source builds (libpcre2-dev/libpq-dev); SQLite ICU ext gcc-compiled from upstream icu.c; PLUS: debian sid chromium 156 (loong64 builds exist in main archive!) + node /usr/local from lcr node + single-file-cli@2.0.75 (system chromium via simple-cdp, no puppeteer download) - snapshots actually work; uBlock stage skipped (cosmetic ad-block for snapshots); bootstrap.sh entrypoint; smoke: /health 200 + login 200
	// --- wave64 (2026-11-21) python trio: trendradar + xiaomusic + filecodebox ---
	"wantcat/trendradar": {Target: loongLocalRegistry + "trendradar"}, // keep tag (6.10.0+6.11.0, source sansan0/TrendRadar v-tag exact match); device build: loong/python:3.14.8 + pip -r deps (PC `uv export --no-dev` from uv.lock; whole litellm tree source-built - aiohttp/cryptography/tiktoken/tokenizers etc have NO cp314 loongarch64 wheels, rust overlay from lcr rust:1.100) + supercronic cross-compiled on PC (aptible/supercronic releases have no loong64 asset); upstream repo ships NO config in store recipe -> image bundles config-default/ + patched entrypoint seeds it on first run; store compose :ro dropped on config mount (seeding needs rw); entrypoint: RUN_MODE cron = supercronic + webserver 8080 serving /app/output
	"hanxi/xiaomusic":    {Target: loongLocalRegistry + "xiaomusic"},    // keep tag (v0.6.1, store version dir 0.6.1); device build per upstream Dockerfile: pdm install --prod --no-editable (PyPI via TUNA, pdm pure python) + npm install npmmirror (nodejs/npm runtime via sid apt; ffmpeg sid apt); C-source builds aiohttp/pillow/pycryptodome/psutil need zlib/jpeg/freetype/lcms2/openjpeg/tiff/webp headers + python3.14-dev; runner copies .venv+node_modules+code, CMD .venv/bin/python3 xiaomusic.py port 8090
	"lanol/filecodebox":  {Target: loongLocalRegistry + "filecodebox"},  // keep tag (store has 2.7.0/2.7.1/2.8.3/2.8.4/2.8.5 - ALL built from matching upstream v-tags, real source per version); PC: two frontend themes prebuilt (vastsa/FileCodeBoxFronted pnpm@9.15.9 frozen -> outDir ../FileCodeBox/themes/2024; Fronted2023 npm --legacy-peer-deps -> dist) then themes copied into each context; device: pip --require-hashes from comment-stripped requirements.lock.txt (aiohttp stack + pydantic-core rust source builds, cargo overlay); APP_VERSION hardcoded per context (store tag); CMD uvicorn main:app port 12345
	// --- wave65 (2026-11-21) python trio 2: domain-admin + calibre-web + qiandao ---
	"mouday/domain-admin":    {Target: loongLocalRegistry + "domain-admin"},    // keep tag (1.6.78/79/80 = PyPI sdists pip-installed per version, upstream repo has NO git tags - store tags mirror PyPI releases); one context, ARG DA_VERSION; deps unpinned resolve on 3.14 clean; CMD gunicorn 'domain_admin.main:app' [::]:8000; volumes /app/database+/app/logs
	"linuxserver/calibre-web": {Target: loongLocalRegistry + "calibre-web"}, // keep tag (0.6.27 = upstream janeczku/calibre-web git tag c3756a9); linuxserver image = s6 overlay no loong64 -> replicate plain launch `python cps.py -p /config/app.db` (linuxserver s6 svc cmdline); range-pinned requirements resolve on 3.14; runtime needs zlib1g+libjpeg62-turbo for source-built Pillow; port 8083
	"a76yyyy/qiandao":        {Target: loongLocalRegistry + "qiandao"},        // keep tag (20250803 = Docker Hub build of master HEAD, upstream git tags stop at 20230111); requirements 2023-era pins bumped 12x for py3.14 (aiohttp 3.13.3/cryptography 46.0.3/greenlet 3.2.4/sqlalchemy 1.4.54/tornado>=6.4/pycryptodome 3.23/cffi 2.0 + pycurl restored, built vs libcurl4-openssl-dev); upstream layout /usr/src/app kept verbatim for store volume mounts; requirements line markers need prefix-match sed (exact-match misses `; python_version` suffixes)
	// --- wave67 (2026-11-21) ebook reader + AI CRM + docs site ---
	"ghcr.io/koodo-reader/koodo-reader": {Target: loongLocalRegistry + "koodo-reader"}, // keep tag (master = upstream CI rev ddd82d0a); amd64 image layer-replication: static React build copied verbatim (arch-free), Go httpserver cross-compiled on PC (pure Go incl modernc sqlite), base loong/caddy:2.11.9 == upstream CADDY_VERSION; ENTRYPOINT [] required (caddy base entrypoint would eat CMD /start.sh)
	"1panel/cordys-crm":                 {Target: loongLocalRegistry + "cordys-crm"},   // keep tag (v1.9.6-f7e69ad); amd64 app-tree extraction (app/ classes+lib+static, mcp jar, cockpit bundle, shells, installer) onto loong/java:21 + node22 (COPY --from loong/node); alpine bundled mariadb/redis replaced by compose sidecars cordys-db(lcr mysql:8.4.3)+redis-svc(lcr redis:7.4.1), installer properties rewritten embedded=false+sidecar hosts; start-all wait-for-it timeouts 120/150s->1800s (java cold start ~20min would starve mcp/cockpit launchers); run-java.sh needs appuser account
	"squidfunk/mkdocs-material":         {Target: loongLocalRegistry + "mkdocs-material"}, // keep tag (9.8.0); device build loong/python:3.14.8 + pip SJTU mirror (PEP668 via PIP_BREAK_SYSTEM_PACKAGES, NO pip upgrade - distro pip has no RECORD and uninstall-no-record-file fails); pillow+cffi source builds need python3-dev libffi-dev libfreetype6-dev; upstream theme-symlink trick + git safe.directory kept
	// --- wave68 (2026-11-21) nocode platform + matrix homeserver + php image host ---
	"nocobase/nocobase":            {Target: loongLocalRegistry + "nocobase"}, // keep tag (2.2.40); amd64 app-tree extraction (yarn workspace, JS-only after stripping x64 napi platform pkgs @napi-rs/canvas-linux-x64-*, @nocobase/license-kit-linux-x64-*; no require sites for canvas); preset builtIn drops @nocobase/plugin-license (license-kit has no loong64 napi target, plugin is commercial gating); apt nginx (trixie) replaces baked amd64 nginx, lcr node base sources rewritten debian-ports->main sid TUNA; base loong/node:26.13.1 = lcr node22.17 trixie + yarn 1.22
	"matrixconstructor/synapse":    {Target: loongLocalRegistry + "synapse"},  // keep tag (v1.164.2); store repo name is a dead mirror, official = matrixdotorg/synapse; device pip build on lcr python:3.13-slim, PyPI has no loong64 wheels -> in-container rustup stable for cryptography/bcrypt sdists; w68 entrypoint auto-generates homeserver config on first start (upstream start.py errors without config); compose volume synapse-data external -> ./data bind
	"halcyonazure/lsky-pro-docker": {Target: loongLocalRegistry + "lsky-pro"}, // keep tag (latest); Laravel 9 app tree extraction (/var/www/lsky) + apache conf templates + snakeoil pair; base lcr php:8.2-apache == upstream PHP 8.2.35 exactly; docker-php-ext gd(freetype+jpeg)/bcmath/pdo_mysql/exif/zip + pecl imagick (clear lcr dead pear proxy first, wave41); AllowOverride All via conf-enabled (conf-enabled loads after apache2.conf, upstream image shipped broken rewrite); apt mirror = TUNA main-archive sid (lcr bases bake stale debian-ports sources)
	// --- wave69 (2026-11-22) ERP + monitoring stack + rust db client ---
	"odoo": {Target: loongLocalRegistry + "odoo"}, // bare repo name (store compose refs library-style odoo:19.0, no namespace); keep tag (19.0); arch-all deb dpkg-deb -x extract (pure python app tree) + sid apt python3-* deps all resolvable except python3-pypdf2 (pip pypdf2==3.0.1 TUNA) and python3-renderpm (dropped in sid, lazy-import only); wkhtmltopdf has no loong64 -> PDF report degrade; rtlcss via npmmirror npm, node-less from apt; official entrypoint/odoo.conf/wait-for-psql.py verbatim; odoo 18.0 dir NOT portable (py<=3.12 era app on sid py3.14) -> left upstream (audit NONLOCAL warning)
	"zabbix/zabbix-server-mysql":    {Target: loongLocalRegistry + "zabbix-server-mysql"}, // keep tag (alpine-7.4.5); C source build on debian sid (configure --enable-server --with-mysql --with-libxml2 --with-libcurl, strip 23.5M->6.7M); runtime sid: libxml2-16 pkg (sid libxml2 pkg ships old soname .so.2, build links .so.16!) + libevent core/extra/pthreads split pkgs + fping + gosu; entrypoint renders zabbix_server.conf from env
	"zabbix/zabbix-java-gateway":    {Target: loongLocalRegistry + "zabbix-java-gateway"}, // keep tag (alpine-latest in compose); jar compiled from bundled sources (javac + 4 bundled jars, NO maven) on temurin 21 alpine-jdk; runtime temurin 21 alpine-jre; tarball settings.sh sets PID_FILE but busybox ps lacks -p -> false "did not start": force console mode PID_FILE=
	"zabbix/zabbix-web-nginx-mysql": {Target: loongLocalRegistry + "zabbix-web-nginx-mysql"}, // keep tag (alpine-latest in compose); ui tree = pure PHP from source tar (arch-free); sid nginx + php8.4-fpm unix socket, entrypoint renders conf/zabbix.conf.php from env (DB_SERVER_*/ZBX_SERVER_*/PHP_TZ)
	"t8y2/dbx": {Target: loongLocalRegistry + "dbx"}, // keep tag (0.6.68); rust dbx-web device build on lcr rust:1.100 (rustc 1.100.0), features sqlite-sqlcipher,mq-admin (cut duckdb-sidecar C++/dynamodb aws-lc/system-fonts font-kit); 4 github git-deps vendored: PC clone at pinned rev + path patches + config.toml [source] directory replacement + [workspace] strips (mysql_async/rust_mysql_common/tokio-postgres-gaussdb/libsqlite3-hotbundle); hotbundle dep deleted from dbx-drivers (libsqlite3-sys feature conflict vs sqlcipher) with no-op feature kept for dbx-core manifest reference; frontend = amd64 image layer /app/static extraction (734 files), DBX_STATIC_DIR env
	// --- wave70 (2026-11-22) monitoring agent + full-stack mail server ---
	"netdata/netdata": {Target: loongLocalRegistry + "netdata"}, // keep tag (v2.12.3); C source build on debian sid replicating official oci Dockerfile (netdata-installer --use-system-protobuf --disable-ebpf --enable-plugin-netflow --internal-systemd-journal; otel-plugin dropped = loong64 degrade); go.d.plugin from bundled src/go with sid golang-go + goproxy.cn; INSTALLER ARG TRAP: --install-no-prefix needs trailing `/ ""` (empty positional is load-bearing for the option-loop final shift, dash `shift: can't shift that many` otherwise); run.sh/health.sh verbatim from packaging/docker; runtime sid: libuv1t64 (t64 rename) + libssl3t64 + libwebsockets19t64 + libprotobuf32t64
	"ghcr.io/docker-mailserver/docker-mailserver": {Target: loongLocalRegistry + "mailserver"}, // keep tag (16.0.1); DMS apt-layer replica on debian sid (w70_packages.sh); rspamd NOT available for loong64 (removed from Debian archives, upstream apt repo amd64/arm64 only) -> DMS auto-falls back to amavisd-new+spamassassin antispam; p7zip-full -> 7zip (sid rename); jaq (update-check only) + step-cli (unused) skipped; swaks/kam.key/postgrey-whitelist/fts-xapian tarballs baked from PC context (github/mcgrail blocked at device build time); dovecot-fts-xapian debuild on device (dovecot-dev + libxapian-dev); ClamAV DB = 3 .cvd files baked from context (arch-free data; database.clamav.net needs curl -A "ClamAV/x.y" UA else Cloudflare HTML block); clamav user pinned uid 200 pre-install; dumb-init from sid pkg; entrypoint/supervisor/dms-healthcheck verbatim
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
		if strings.HasPrefix(image, loongLocalRegistry) {
			// offline-mirror store packages arrive with the local-registry
			// ref pre-baked (compose ships 127.0.0.1:5000/loong/...); when
			// that repo:tag is absent locally, swap to the public ACR mirror
			// so the install still works online
			mapped := withLoongACRFallback(image)
			if mapped != image {
				svc["image"] = mapped
				global.LOG.Infof("[loong64] app service %q image %s -> %s (pre-baked ref, ACR fallback)", name, image, mapped)
			}
			continue
		}
		mapped, ok := mapLoong64Image(image)
		if !ok {
			continue
		}
		mapped = withLoongACRFallback(mapped)
		svc["image"] = mapped
		global.LOG.Infof("[loong64] app service %q image %s -> %s", name, image, mapped)
	}
	return nil
}

func isBlockedLoong64Image(image string) bool {
	repo, _ := splitImageRef(image)
	return loong64BlockedRepos[repo]
}

// withLoongACRFallback swaps a local-registry image ref to the public ACR
// mirror when the local registry does not have that repo:tag (or is
// unreachable). Only self-built images (loongLocalRegistry targets) fall
// back — lcr targets are publicly pullable and never need it.
func withLoongACRFallback(image string) string {
	if !strings.HasPrefix(image, loongLocalRegistry) {
		return image
	}
	rest := strings.TrimPrefix(image, loongLocalRegistry) // "<name>:<tag>"
	// registry repo path is loong/<name> under the host part
	if localRegistryHas(loongRepoNamespace + rest) {
		return image
	}
	alt := loongACRRegistry + rest
	global.LOG.Infof("[loong64] local registry miss %s -> ACR fallback %s", image, alt)
	return alt
}

// localRegistryHas probes the local registry for a repo:tag manifest via
// HEAD. Only a confirmed 200 counts as present; anything else (404,
// registry down, timeout) means "miss" so the install proceeds from the
// ACR mirror instead of failing.
func localRegistryHas(repoTag string) bool {
	repo, tag := splitImageRef(repoTag)
	if repo == "" || tag == "" {
		return false
	}
	req, err := http.NewRequest(http.MethodHead,
		fmt.Sprintf("%s/v2/%s/manifests/%s", loongLocalProbeBase, repo, tag), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
	}, ", "))
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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
