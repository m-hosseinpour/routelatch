# تحقیق و شواهد

بررسی از ۲۰۲۶-۱۱-۱۶ تا ۲۰۲۶-۱۱-۱۷. این سند **snapshot تحقیق** است، نه گزارش ممیزی
کامل یا نتیجهٔ اجرای RouteLatch. مراجعه به نسخه‌های جدید باید ادعاهای وابسته را باز کند.

## دامنه و روش

دو بررسی مستقلِ فقط‌خواندنی برای کدهای قبلی و یک بررسی و پیگیری برای API/سورس
Xray انجام شد. تصمیم‌های معماری توسط هماهنگ‌کننده با بررسی مستقیم منابع تعیین‌کننده
جمع‌بندی شدند. هیچ اپ، آزمایش proxy، نصب، download binary، تغییر سرویس یا درخواست
با credential انجام نشد. خواندن سرویس فعلی و metadata مجوز فایل‌ها تغییر وضعیت نیست.

جست‌وجو از اتصال موجود websearch انجام شد؛ ابزار مستقل TinyFish و تحقیق پولی استفاده
نشد. جست‌وجوهای هم‌زمان اولیه به محدودیت in-flight رسیدند و نتیجهٔ منفی تلقی نشدند؛
جست‌وجوهای بعدی ترتیبی بودند. صفحه‌های developer.android.com قابل دریافت نبودند
(403 با علت نامشخص)؛ از منبع عمومی AOSP استفاده شد. صفحهٔ pkg.go.dev یک کتابخانه
نیز قابل خواندن نبود؛ منبع عمومی GitHub خوانده شد. امنیت-policy block دور زده نشد.

## پیاده‌سازی‌های قبلی — شواهد داخلی

| مرجع | وضعیت سورس | حوزهٔ خوانده‌شده |
| --- | --- | --- |
| `agent-skills` | HEAD `9081b5a95913580984554beda4e3dd94ffbdd63d`؛ locators مربوط به فایل‌های خوانده‌شدهٔ working copy، نه baseline اجرایی | `skills/targeted-proxy-access/{SKILL.md,references/setup.md,scripts/tunnel.py,scripts/launch.py}` |
| `agent-websearch` | HEAD `efde2e2`؛ بررسی مستقل worktree را clean گزارش کرد | egress router/tunnel، config، CLI، deploy، backends و تست‌های مرتبط |

پروژه‌ها فقط مرجع‌اند. هیچ share link، فایل env/token، محتوا یا مقصد شخصی در این
سند ثبت نشده است. عنوان‌ها و آمار سرویس محلی اثبات دسترسی زنده به اینترنت نیستند.

### قابلیت‌های قابل انتقال

| حوزه | اسکیل | websearch | نتیجه برای RouteLatch |
| --- | --- | --- | --- |
| مسیر | domain exceptions در Xray، direct پیش‌فرض | انتخاب مسیر در fetcher | فهرست مشترک مقصد و حالت صریح |
| pool | seed از v2rayN، last-good، scan، cooldown، rerank گردشی، hysteresis، backup/rollback | شروع lazy، candidate limit و cooldown | حفظ قابلیت‌ها با یک مالک وضعیت و ثبت کیفیت جدا |
| تشخیص | fetch، learn، relearn با مقایسهٔ مستقیم/تونل | direct failure + tunnel success در GET | پیشنهاد تأییدمحور، بدون replay و بدون auto unlearn |
| خطا/بودجه | canary و retryهای محدود | detector پیش از routing، reserve برای tunnel | تفکیک network/origin و محدودکردن بودجهٔ probe |
| عملیات | status/list/domains/doctor/env، up/down/serve، wrapper، pause/resume، نصب/حذف سرویس | setup/status/doctor، CLI و foreground/systemd | CLI و منوی مستقل با تشخیص، چهار حالت و مالکیت روشن |
| توزیع | discovery/download/checksum موتور و چند مسیر پلتفرمی | wheel و ابزار نصب Python، lifecycle تونل POSIX | بستهٔ Go خودکفا، بدون وابستگی به فایل‌های skill |

### محدودیت‌های تعیین‌کننده

- gateway اسکیل: mixed loopback، UDP خاموش، `domainStrategy: AsIs`؛ hostname-only
  است. `tunnel.py:510–523` مستقیم خوانده شد.
- `allow/disallow/learn` تغییر فایل و restart gateway دارند؛ probe سلامت gateway
  می‌تواند از rule list اثر بگیرد. منبع: `tunnel.py:549–565,1336–1364,1499–1605`.
- اسکیل `gateway.json` را بدون private-write می‌نویسد (`702–711`) و `status`
  prefix لینک را چاپ می‌کند (`1253–1258`). روی نصب محلی بررسی metadata، مجوز پوشه
  0775 و pool/gateway 0664 مشاهده شد؛ محتوا ثبت نشد. این مشکل ماشین فعلی است، نه
  حکمی دربارهٔ تمام نصب‌ها. اصلاح آن خارج از این کار بود.
- wrapper فعلی سلامت را با بازبودن پورت می‌سنجد و در نبود gateway مستقیم اجرا می‌کند؛
  این رفتار با D11 ناسازگار است. `launch.py:46–59,76–97`.
- router websearch فقط `.get()` دارد، redirect را در HTTP client دنبال می‌کند و
  hostname اصلی را برای learning نگه می‌دارد. `egress/router.py:125–134,160–245`.
- detector می‌تواند خطای HTTP را terminal کند؛ بدون detector، 403/451 مسیر بعدی را
  امتحان می‌کند. blanket claim «CAPTCHA با tunnel درست می‌شود» یا «هیچ CAPTCHA
  هرگز با خروجی دیگری تغییر نمی‌کند» از این کد اثبات نمی‌شود. `204–240`.
- learned list دائمی، suffix-based و فقط زمان init بار می‌شود؛ خطا در pool دامنهٔ
  آموخته‌شده را خودکار direct نمی‌کند. `router.py:25–98,149–158`.
- فایل‌های credential در websearch private-write بهتری دارند، اما README دربارهٔ
  تمام فایل‌های دستی/قدیمی بیش از کد تضمین می‌دهد؛ `config.py:228–243` و
  `cli.py:111–136` باید هنگام الگوبرداری لحاظ شوند.

این بررسی defect-only جامع نبود. تست‌های پروژه‌ها خوانده شدند، **اجرا نشدند**؛
benchmarks، مشکلات parser واقعی سایت و رفتار Windows/macOS قبلی آزموده نشدند.

## شواهد عمومی تعیین‌کننده

| ID | ادعا و پشتیبانی خوانده‌شده | وضعیت و اثر |
| --- | --- | --- |
| E01 | [Xray API docs](https://xtls.github.io/en/config/api.html): HandlerService افزودن/حذف outbound؛ RoutingService افزودن/جایگزینی قوانین | مستندات زنده؛ به معنی session-close یا success event نیست |
| E02 | [balancing.go](https://raw.githubusercontent.com/XTLS/Xray-core/main/app/router/balancing.go) و [dispatcher](https://raw.githubusercontent.com/XTLS/Xray-core/main/app/dispatcher/default.go): بالانسر خالی بدون fallback خطا می‌دهد و dispatcher می‌تواند default handler را بگیرد | سورس moving main خوانده شد؛ reject fallback/تست عدم direct ضروری است |
| E03 | [core/functions.go در v26.11.9](https://raw.githubusercontent.com/XTLS/Xray-core/v26.11.9/core/functions.go): `core.Dial` پس از `Dispatch` یک connection مبتنی بر link برمی‌گرداند | سورس نسخهٔ مشخص؛ برگشت Dial اثبات اتصال مقصد نیست |
| E04 | [context.go:117–129](https://raw.githubusercontent.com/XTLS/Xray-core/v26.11.9/common/session/context.go) و [handler.go:208–260](https://raw.githubusercontent.com/XTLS/Xray-core/v26.11.9/app/proxyman/outbound/handler.go): feedback خطای outbound در context، فیلتر EOF/لغو و interrupt link | مسیر source-inferred برای adapter؛ TLS/reset/teardown/mux واقعی آزموده نشده |
| E05 | [Dispatcher interface](https://raw.githubusercontent.com/XTLS/Xray-core/v26.11.9/features/routing/dispatcher.go) stable annotation؛ [Routing RPC](https://raw.githubusercontent.com/XTLS/Xray-core/v26.11.9/app/router/command/command.proto) و [Handler RPC](https://raw.githubusercontent.com/XTLS/Xray-core/v26.11.9/app/proxyman/command/command.proto) session-close ندارند | stock subprocess به‌تنهایی شرط قطع انتخابی را ثابت نمی‌کند؛ frontend خودمان لازم است |
| E06 | [API docs](https://xtls.github.io/en/config/api.html) برای `sib` disconnect می‌گوید، ولی [source_ip_block.go](https://raw.githubusercontent.com/XTLS/Xray-core/main/main/commands/all/api/source_ip_block.go) فقط rule اضافه می‌کند؛ [RemoveHandler](https://raw.githubusercontent.com/XTLS/Xray-core/main/app/proxyman/outbound/outbound.go) handler را نمی‌بندد | اختلاف doc/source؛ از sib برای قطع زنده استفاده نشود |
| E07 | [commander.go](https://raw.githubusercontent.com/XTLS/Xray-core/main/app/commander/commander.go): Unix socket پشتیبانی و `grpc.NewServer()` بدون auth interceptor؛ [infra/conf/api.go](https://raw.githubusercontent.com/XTLS/Xray-core/main/infra/conf/api.go) ObservatoryService را ثبت می‌کند | سورس main؛ مدیریت خصوصی ممکن است، ولی API loopback بی‌auth مرز امن نیست |
| E08 | [curl proxy types](https://curl.se/libcurl/c/CURLOPT_PROXYTYPE.html): SOCKS5_HOSTNAME حل نام توسط proxy؛ [go-socks5 handle.go:55–64](https://raw.githubusercontent.com/things-go/go-socks5/master/handle.go) پیش از custom dial resolve می‌کند | انتخاب کتابخانه باید remote hostname را حفظ کند، نه فقط WithDial داشته باشد |
| E09 | [libXray README](https://github.com/XTLS/libxray): API stability تضمین نمی‌شود؛ parse/projection و حفظ opaque XHTTP extra؛ validation sandbox نیست؛ چند core/probe با وضعیت process-global تداخل دارند | نامزد reuse با نسخهٔ pinned؛ نه adapter انتخاب‌شده و نه مجوز اجرای config اشتراک |
| E10 | [Xray TUN docs](https://xtls.github.io/en/config/inbounds/tun.html) و [Android source در v26.11.9](https://raw.githubusercontent.com/XTLS/Xray-core/v26.11.9/proxy/tun/tun_android.go): TUN و FD خارجی برای Android | اصل زیرساخت در نسخهٔ مشخص گزارش/بررسی سورس دارد؛ گزینه‌های جدید صفحه لزوماً متعلق به همان نسخه نیستند |
| E11 | [RFC 9110، §9.2.2 و §9.3.6](https://rfc-editor.org/rfc/rfc9110.html): عدم retry خودکار non-idempotent توسط proxy و blind forwarding بعد از CONNECT | حفظ قرارداد HTTP و no replay/no MITM |
| E12 | [RFC 9849، §1/§3](https://rfc-editor.org/rfc/rfc9849.html)، منتشرشده مارس ۲۰۲۶: ECH از SNI و سایر fields حفاظت می‌کند | sniffing نمی‌تواند همیشه دامنهٔ اصلی را اثبات کند |
| E13 | [AOSP VpnService.java](https://android.googlesource.com/platform/frameworks/base/+/b6f2f924db65f74da85f0beb909bb26584af2768/core/java/android/net/VpnService.java): یک VPN فعال، foreground requirement، protect socket و per-app allow/disallow | snapshot سورس؛ Android پوستهٔ بومی و محدودیت VPN دارد |
| E14 | [Mozilla MPL FAQ، Q8/Q11/Q13](https://mozilla.org/MPL/2.0/FAQ): larger work/static linking ممکن است با مجوز دیگر باشد، covered source همچنان تعهد دارد | مجوز محصول باز؛ وابستگی‌های build واقعی پیش از انتشار audit شوند |
| E15 | TUN در Xray pin‌شدهٔ همین repo: `proxy/tun/*` (config، Linux route/DNS، fd خارجی، stack سبک، `autoOutboundsInterface`) و `infra/conf/tun.go`/`xray.go` در commit `676eaf66f8f0` (pseudo-version `v1.260327.1-0.20261109052709-676eaf66f8f0`) خوانده شد | زیرساخت TUN در نسخهٔ pin‌شده وجود دارد؛ runtime آن توسط ما آزموده نشده و R16-A باید آن را ثابت کند |

Xray `v26.11.9` در [release metadata](https://api.github.com/repos/XTLS/Xray-core/releases/tags/v26.11.9)
با تاریخ انتشار ۲۰۲۶-۱۱-۰۹ مشاهده شد. [go.mod آن](https://raw.githubusercontent.com/XTLS/Xray-core/v26.11.9/go.mod)
`go 1.27` و مجموعهٔ وابستگی‌ها را نشان می‌دهد؛ ساخت واقعی انجام نشده است. این نسخه
مرجع تحقیق است، نه نسخهٔ الزام‌آور release RouteLatch. انتخاب نسخه و toolchain در T1.

## شواهد TUN و پیش‌نیازهای آن (۲۰۲۶-۱۱-۲۶، R16)

این بخش snapshot بررسی طراحی D29 است؛ هیچ کد، سرویس، TUN یا route واقعی در این
جلسه تغییر نکرد. مرجع اصلی، سورس **نسخهٔ pin‌شدهٔ همین repo** است، نه صفحهٔ زندهٔ
docs: pseudo-version `v1.260327.1-0.20261109052709-676eaf66f8f0` (commit
`676eaf66f8f0`) در module cache، مسیرهای `proxy/tun/`, `infra/conf/tun.go` و
`infra/conf/xray.go`.

**قابلیت‌های خوانده‌شده در سورس pin‌شده:**

- `protocol: "tun"` inbound بدون port/listen پذیرفته می‌شود؛ فیلدهای settings
  شامل `name`، `mtu`، `gateway` (multi-prefix IPv4/IPv6)، `dns`، `userLevel`،
  `autoSystemRoutingTable`، `autoOutboundsInterface`، `desc`،
  `autoSystemDnsToGateway` و `autoSystemWfpBlockLeak` است.
- Linux مسیر عادی: ساخت TUN، assign آدرس‌های `gateway`، routeهای
  `autoSystemRoutingTable` با priority 1، و `autoOutboundsInterface` که dialهای
  خود core را با `SO_BINDTODEVICE` به interface فیزیکی bind می‌کند (loopback
  مستثنا). با `xray.tun.fd`/`XRAY_TUN_FD` یک fd خارجی (IFF_TUN|IFF_NO_PI،
  اختیاری vnet hdr) پذیرفته می‌شود و در آن حالت route/DNS setup انجام نمی‌شود.
- `autoSystemDnsToGateway` روی Linux از `resolvectl` و systemd-resolved استفاده
  می‌کند، به `gateway` نیاز دارد، پیش از تغییر DNS یک preflight مسیر DNS انجام
  می‌دهد، در نبود پیش‌نیازها **شروع نمی‌کند** و در `SIGKILL` مقدار DNS را برنمی‌گرداند.
  بنابراین D29 این گزینه را خاموش نگه می‌دارد.
- TUN handler هر flow را با `session.Content{SniffingRequest}` به dispatcher
  می‌دهد؛ `sniffing` استاندارد inbound روی TUN هم اعمال می‌شود و dispatcher
  می‌تواند مقصد را به دامنهٔ sniff‌شده override کند تا outbound SOCKS دامنه را
  ببیند. این مسیر source-inferred است و باید با ترافیک واقعی آزموده شود.
- `internet.RegisterDialerController` یک controller را به slice سراسری process
  اضافه می‌کند و API حذف ندارد؛ پس افزودن/حذف TUN در همان process engine
  effect دائمی می‌گذارد. این دلیل اصلی جداکردن TUN به process سرویس است.
- TUN handler در سطح info مقصد/مبدأ را log می‌کند؛ برای D21 باید error/access
  sink خاموش و سطح log محدود بماند و تست منفی log وجود داشته باشد.
- README خود TUN هشدار می‌دهد که این قابلیت برای مدیر شبکه است، بدون route
  درست می‌تواند loop/router lock-up بسازد، TCP را پیش از اتصال مقصد SYN-ACK
  می‌کند (پس خطای pool به‌صورت close دیده می‌شود، نه refuse)، ICMP فقط Echo را
  محلی پاسخ می‌دهد، و DNS همیشه جدا از لایهٔ ۳ است.

**پیش‌نیاز capability (بررسی محلی، نه تعمیم):** روی این host (kernel
`7.0.0-44-generic`، uid 1000، `CapEff=0`) یک تست C کوچک نشان داد
`setsockopt(SO_BINDTODEVICE, "lo")` موفق و `SO_MARK` با `EPERM` رد می‌شود.
منبع عمومی (kernel commit `c427bfec18f2`، از Linux 5.7) همین را تأیید می‌کند:
bind به interface دیگر برای کاربر غیرroot مجاز است، در حالی که ساخت TUN و تغییر
route همچنان `CAP_NET_ADMIN` می‌خواهد. نتیجهٔ طراحی: engine غیرroot با
`SO_BINDTODEVICE` از TUN خارج می‌شود و فقط سرویس TUN کوچک capability می‌گیرد.
این مشاهده فقط یک host است؛ R16-A باید kernel/distro matrix را محدود و مستند کند.

**sidecar:** docs فعلی sing-box `route.default_interface` (و
`auto_detect_interface`) را برای جلوگیری از loop زیر TUN معرفی می‌کند؛ پشتیبانی
دقیق نسخهٔ pin‌شدهٔ `v1.14.4` و شکل فیلد باید در R16-A از سورس/باینری همان نسخه
تأیید شود. این یک شکاف باز است، نه فرض قطعی.

**محدودیت‌ها:** هیچ TUN واقعی ساخته/آزموده نشد؛ sniffing، dual-stack، DNS
مستقیم، UDP block، sidecar bypass و fail-closed فقط از سورس استنباط شده‌اند.
صفحه‌های زندهٔ docs ممکن است به نسخهٔ دیگری تعلق داشته باشند. R16-A دروازهٔ
اجرای کنترل‌شده است و تا آن زمان D29 یک طراحی است، نه قابلیت اثبات‌شده.

## مقایسهٔ گزینه‌های عمومی

| گزینه | شاهد خوانده‌شده | جمع‌بندی محدود |
| --- | --- | --- |
| Xray | E01–E10 و [XHTTP reference](https://github.com/XTLS/Xray-core/discussions/4113) | سازگاری transport و مسیر Go مناسب‌تر برای نیاز فعلی؛ نیازمند frontend/adapter و آزمون |
| sing-box | [transportها](https://sing-box.sagernet.org/configuration/shared/v2ray-transport/)، [URLTest](https://sing-box.sagernet.org/configuration/outbound/urltest/)، [mixed](https://sing-box.sagernet.org/configuration/inbound/mixed/)، [API](https://sing-box.sagernet.org/configuration/service/api) | TUN/DNS/انتخاب آماده؛ فهرست رسمی transportها هم‌ارزی کامل XHTTP را اثبات نمی‌کند؛ API مستند mutation معادل Xray در بررسی یافت نشد، نه اثبات نبود همهٔ مسیرهای داخلی |
| mihomo | [fallback](https://wiki.metacubex.one/en/config/proxy-groups/fallback)، [URL-test](https://wiki.metacubex.one/en/config/proxy-groups/url-test)، [COMPATIBLE](https://wiki.metacubex.one/en/config/proxies/built-in/) و [group config](https://wiki.metacubex.one/en/config/proxy-groups) | الگوی سلامت/انتخاب؛ COMPATIBLE معادل DIRECT است و empty-group policy باید کنترل شود |
| GOST | [Bypass](https://gost.run/en/concepts/bypass) و [Load balancing](https://gost.run/en/concepts/load-balancing) | الگوی جداسازی سیاست، انتخاب و hot reload؛ هم‌ارزی transport pool اثبات نشده |
| COW | [README](https://github.com/cyfdecyf/cow/blob/master/README-en.md) | مستقیم اول، retry، سابقه، استثنا و PAC؛ ایدهٔ مشابه، نه موتور انتخاب‌شده یا پروژهٔ امنیت‌سنجی‌شده |
| OpenTUI | [README](https://github.com/anomalyco/opentui): Zig و TypeScript/React/Solid | برای TUI جدی نامزد است؛ با تصمیم منوی سبک نیازی به runtime جدا نداریم |
| huh | [README](https://github.com/charmbracelet/huh): forms/prompts، standalone و accessible mode | نامزد منوی سبک Go؛ هنوز انتخاب/آزمون نشده — **[به‌روزرسانی ۲۰۲۶-۱۱-۱۷]** `huh v1.0.0` انتخاب و pin شد؛ sanitizer خودمان نشت control sequence را می‌پوشاند |
| goproxy | [README](https://github.com/elazarl/goproxy)، [proxy.go](https://raw.githubusercontent.com/elazarl/goproxy/master/proxy.go)، [https.go](https://raw.githubusercontent.com/elazarl/goproxy/master/https.go): CONNECT، custom transport/dial، hijack | نامزد frontend؛ MITM اختیاری باید خاموش باشد؛ fit و retry/stream semantics آزمون می‌خواهد |

## سیستم‌عامل و service manager

[sing-box client guide](https://sing-box.sagernet.org/manual/proxy/client) فرق system
proxy، firewall redirect و virtual interface را توضیح می‌دهد. پروکسی دسکتاپ همهٔ TCP
یا UDP را اجباراً capture نمی‌کند.

[Windows SCM](https://learn.microsoft.com/en-us/windows/win32/services/service-control-manager)
مدیریت service بومی را تعریف می‌کند و [WinHTTP/WinINet](https://learn.microsoft.com/en-us/windows/win32/winhttp/setting-wininet-proxy-configurations-in-winhttp)
تفاوت استفادهٔ برنامه‌ها از تنظیمات proxy را نشان می‌دهد. طرح: core قابل حمل، lifecycle
بومی. هیچ Windows runtime check، Android build یا آزمایش systemd hardening انجام نشد.

## نامزدهای tray/StatusNotifierItem (بررسی ۲۰۲۶-۱۱-۲۳)

هدف این بررسی، امکان‌سنجی یک tray بدون شکستن باینری static (`CGO_ENABLED=0`) بود.
هیچ کتابخانه‌ای در این تاریخ انتخاب، pin یا روی میزبان اجرا نشد؛ R13-A باید prototype
و آزمون واقعی KDE را انجام دهد.
**[به‌روزرسانی ۲۰۲۶-۱۱-۲۳]** این پاراگراف snapshot پیش از R13-A است. در R13-A هر دو
نامزد روی KDE اجرا شدند و `deedles.dev/tray v0.2.0` pin شد؛ بخش «نتیجهٔ spike tray»
پایین‌تر.

| نامزد | شاهد/نسخه | نتیجه | محدودیت |
| --- | --- | --- | --- |
| `gogpu/systray` | [README](https://github.com/gogpu/systray)، releases تا `v0.3.0` (2026-08-30) | MIT، pure Go/zero CGO، پیاده‌سازی Linux با StatusNotifierItem/D-Bus، منو، آیکن PNG و نوتیفیکیشن | پروژهٔ جوان است (اولین انتشار 2026-04)؛ API/پایداری و رفتار DEها باید با prototype سنجیده شود |
| `deedles.dev/tray` | [pkg.go.dev](https://pkg.go.dev/deedles.dev/tray)، [GitHub](https://github.com/DeedleFake/tray)، آخرین tag بررسی‌شده `v0.2.0` (2026-10-28؛ tag در GitHub API تأیید شد) | MIT، pure Go، پیاده‌سازی مینیمال StatusNotifierItem | سطح API کوچک‌تر؛ نوتیفیکیشن/منوی پیشرفته و maturity باید بررسی شود؛ نسخهٔ نهایی باید در R13-A pin شود |
| `fyne-io/systray` | [README](https://github.com/fyne-io/systray) | fork از getlantern با پشتیبانی Linux از طریق D-Bus/SNI | README صریحاً cgo لازم دارد؛ با build فعلی `CGO_ENABLED=0` سازگار نیست |
| `getlantern/systray` | [README](https://github.com/getlantern/systray) | tray cross-platform شناخته‌شده | cgo/GTK روی Linux؛ گزینهٔ static نیست |

نکات محیطی: KDE Plasma مسیر آزمون اولیه است. GNOME به‌طور پیش‌فرض
StatusNotifierItem را نشان نمی‌دهد و به extension (مثل AppIndicator) نیاز دارد؛
SSH/بدون display هم tray ندارد. XEmbed قدیمی انتخاب نمی‌شود. `--headless` تنها
fallback صریح است و fallback بی‌صدا مجاز نیست (D26/R-16). قبل از انتخاب نهایی باید
license/deps، cross-build چهار معماری، ثبت SNI، منو و رفتار parent-death بررسی و
نسخه pin شود.

## نتیجهٔ spike tray pure-Go (۲۰۲۶-۱۱-۲۳، R13-A)

این بخش snapshot شواهد اجرایی R13-A است و ادعاهای امکان‌سنجی جدول بالا را به نتیجه
می‌رساند؛ لاگ‌های خام و دستورها در `spike/tray/FINDINGS.md` و `spike/tray/evidence/`
هستند. هیچ سرویس/autostart/تنظیم دسکتاپ تغییر نکرد و هیچ آیتم tray موجودی دست نخورد.

| نامزد | نسخه و commit | build static چهار معماری | KDE واقعی (پلاسما ۶.۶.۷) | watcher/host غایب | license/deps |
| --- | --- | --- | --- | --- | --- |
| `deedles.dev/tray` | `v0.2.0` = `acf85aba49de` | هر چهار معماری، بدون cgo؛ `file` برای هر ۲۰ آرتیفکت و `ldd`/`go version -m` برای باینری‌های amd64 | ثبت SNI، آیکن ۲۲×۲۲، منوی dbusmenu (مسیر `/StatusNotifierMenu`)، کلیک منو، نوتیفیکیشن با godbus، حذف هنگام خروج؛ plasmashell خودش `GetAll` و `GetLayout` را صدا زد | `New()` برای نبود watcher خطا می‌دهد؛ نبود host با probe برنامه exit ۵ | MIT؛ فقط godbus (BSD-2) و x/sys (BSD-3) لینک می‌شوند |
| `gogpu/systray` | `v0.3.0` = `a71468fe` | همان | همان رفتار SNI/منو/آیکن؛ `Id` را نادیده می‌گیرد (نام bus)؛ نوتیفیکیشن داخلی اما خطای آن مشاهده‌ناپذیر | `New/Create` نبود watcher را فقط warn می‌کند و خطا نمی‌دهد؛ probe برنامه لازم است | MIT؛ همان دو وابستگی؛ goffi فقط در module graph (داروین) |

**تصمیم:** `deedles.dev/tray v0.2.0` برای R13-B pin شد. دلیل: خطاهای صریح در ساخت و
عملیات (به‌جای موفقیت بی‌صدا) و حفظ `Id` درخواستی؛ هر دو نامزد دقیقاً godbus و x/sys
را لینک می‌کنند، پس حجم وابستگی معیار تفکیک نیست. هزینه‌ها: نوتیفیکیشن ندارد (یک
فراخوانی godbus)، پس از restart شدن watcher خودش دوباره ثبت نمی‌شود (supervisor باید
tray child را جایگزین کند؛ مطابق D26)، پروژه کوچک است، و در خطای `New()` اتصال bus را
نمی‌بندد؛ بنابراین retry باید با جایگزینی process باشد نه حلقهٔ `New()`.
`gogpu/systray v0.3.0` گزینهٔ جایگزین مستند است اگر re-registration خودکار مهم‌تر
باشد؛ در آن صورت probe بیرونی و `Remove()` idempotent (باگ panic مشاهده‌شده) لازم است.

**اندازه‌گیری‌های lifecycle:** supervisor یک pidfd روی خودش را پیش از fork به child
می‌دهد؛ child ۱۴–۱۹ میلی‌ثانیه پس از `SIGKILL` شدن supervisor خارج شد (هم در محیط بدون
D-Bus و هم با tray واقعی) و آیتم از watcher حذف شد. حتی وقتی supervisor ۵۰ میلی‌ثانیه
پس از spawn مرد و child با ۵۰۰ میلی‌ثانیه تأخیر arm شد، pidfd ارث‌رسیده مرگ را نشان داد
و child ۴۵۳ میلی‌ثانیه بعد (در لحظهٔ arm) خارج شد، نه روی parent جدید. خروج خودکار
child در ۲.۰ ثانیه توسط supervisor با `waitpid` تشخیص داده شد. مسیر pidfd ناموجود
(`ENOSYS`/`EPERM`) پیاده اما اجرا نشده است.

**آزمون‌نشده:** تأیید بصری آیکن/منو/نوتیفیکیشن توسط کاربر؛ GNOME بدون extension یا
SSH واقعی (فقط bus ایزوله با fake watcher)؛ DEهای دیگر؛ اجرای runtime روی
`riscv64`/`ppc64le`؛ زنده‌ماندن پس از restart واقعی `kded`/plasmashell؛ و ادغام در
بسته/`run` که کار R13-B است.

## فرض‌های اصلاح‌شده و پوشش باقی‌مانده

- فرض «Xray برای TUN موتور دیگری لازم دارد» با شواهد جدید اصلاح شد؛ Android همچنان
  اپ بومی می‌خواهد و رفتار session registry آن باز است. **[به‌روزرسانی ۲۰۲۶-۱۱-۲۶]**
  TUN در D29/R16 به‌عنوان لایهٔ پوشش (نه mode پنجم) تعریف شد و مسیر پیشنهادی،
  TUN inbound همان هستهٔ pin‌شده در یک سرویس جدا است؛ runtime آن همچنان آزموده
  نشده و R16-A دروازه است.
- فرض «subprocess + API همهٔ مدیریت اتصال را حل می‌کند» اصلاح شد؛ E03–E06 مسیر
  استفادهٔ کتابخانه‌ای/frontend را ضروری یا نیازمندی ساده‌تر را محتاج تأیید می‌کند.
- گزینهٔ موقت «همهٔ IPهای نامشخص pool» با تصمیم D10 جایگزین شد.
- منابع زندهٔ main به‌عنوان سند release قطعی تلقی نشوند؛ در T1 نسخه pin و شواهد بسته شود.
- آزمون‌های throughput/پایداری، handshake و error classification واقعی، license audit
  وابستگی‌ها، امنیت frontend و تازه‌بودن قالب دیتابیس v2rayN هنوز انجام نشده‌اند.
  **[به‌روزرسانی ۲۰۲۶-۱۱-۱۷]** دروازهٔ handshake و طبقه‌بندی خطا در `T1` اجرا و
  تصمیمش در [قراردادها](contracts.md) ثبت شد؛ شاهد در `spike/xray/FINDINGS.md`.
  license audit (A14) و throughput همچنان انجام نشده‌اند. متن بالا snapshot همان
  تاریخ است و بازنویسی نشده.
  **[به‌روزرسانی ۲۰۲۶-۱۱-۱۸]** ممیزی مجوز/وابستگی `A14` در `R3` انجام شد: مجوز
  Apache-2.0 و فهرست ۶۸ ماژول لینک‌شده در [مجوز و ممیزی](licensing.md) ثبت است.
  throughput، آزمون امنیت frontend و تازگی قالب v2rayN همچنان انجام نشده‌اند؛ متن
  بالا snapshot تاریخ خودش است.
- بررسی نام `RouteLatch` فقط یک جست‌وجوی اولیه بود؛ برخورد واضح پروژهٔ proxy در
  نتایج مشاهده نشد. تضمین یکتایی، availability رجیستری یا علامت تجاری نیست.

## بررسی نام کوتاه `rlch` (۲۰۲۶-۱۱-۲۲)

این بررسی فقط برای انتخاب نام نصب‌شدهٔ کوتاه بود و ادعای یکتایی جهانی یا علامت
تجاری نمی‌کند. هر دو نام `rlch` و `rtlch` جست‌وجو شدند؛ `rlch` انتخاب شد چون
پیشوند `rtl` با ابزارهای `rtl-sdr` و معنای right-to-left شلوغ است.

| منبع | روش/URL | نتیجه برای نام دقیق |
| --- | --- | --- |
| Debian stable و sid | `https://deb.debian.org/debian/dists/{stable,sid}/main/Contents-amd64.gz`، سپس `zgrep` برای مسیرهای دقیق `/rlch` و `/rtlch` | هیچ باینری هم‌نام |
| Ubuntu noble | `https://archive.ubuntu.com/ubuntu/dists/noble/Contents-amd64.gz`، سپس `zgrep` | هیچ باینری هم‌نام |
| Arch رسمی | `https://archlinux.org/packages/search/json/?q=rlch` (و `rtlch`) | ۰ بسته |
| AUR | `https://aur.archlinux.org/rpc/v5/search/rlch` (و `rtlch`) | نام دقیق پیدا نشد؛ جست‌وجوی fuzzy فقط `r-urlchecker` را دید |
| Fedora/EPEL | `https://packages.fedoraproject.org/search?query=rlch` برای Fedora 43/44/45، Rawhide و EPEL 8/9/10 | ۰ بسته |
| npm | `https://registry.npmjs.org/rlch` (و `rtlch`) | 404 |
| PyPI | `https://pypi.org/pypi/rlch/json` (و `rtlch`) | 404 |
| crates.io | `https://crates.io/api/v1/crates/rlch` (و `rtlch`) | 404 |

محدودیت‌ها: `Contents-all` دبیان به‌دلیل timeout و `Contents-all` اوبونتو با 404
بررسی نشد؛ فقط لیست‌های `Contents-amd64` دبیان/اوبونتو و نام بسته‌های Arch/Fedora
دیده شدند. این بررسی مخازن شخصی، PPA، COPR، Homebrew و همهٔ توزیع‌ها را پوشش نمی‌دهد.
`rlch` در GitHub یک نام کاربری است، نه یک دستور نصب‌شده.

## نتیجهٔ spike منوی tray (۲۰۲۶-۱۱-۲۵، R15-A)

این snapshot شاهد R15-A است و وضعیت نسخه‌های پیشین یا R13-A را بازنویسی نمی‌کند.
محدوده: `deedles.dev/tray v0.2.0` در محیط واقعی **KDE Plasma 6.6.7 / kwin_x11 6.6.4 /
X11**، session bus `unix:path=/run/user/1000/bus`، Go 1.27.4 با `CGO_ENABLED=0`.
پروتوتایپ `spike/tray/cmd/menu-spike` و اسکریپت‌های `menu-offline.sh` و
`kde-menu-evidence.sh` فقط شاهد هستند؛ هیچ کد محصول، سرویس، autostart یا تنظیم
دسکتاپ تغییر نکرد. جزئیات و locatorهای خام در
[`spike/tray/MENU-FINDINGS.md`](../spike/tray/MENU-FINDINGS.md) و فایل‌های
`spike/tray/evidence/menu-*-2026-11-25.*` ثبت شده‌اند.

**نتیجه‌ها (با شاهد D-Bus؛ تصویرهای menu برای تغییرهای زنده):**

- زیرمنوی تودرتو (root → Pool → Healthy (120) → ۲۰ ردیف) و separatorها: کار می‌کند.
  layout عمق ۳ و `GetLayout(45/50,1)` توسط خود plasmashell ثبت شد؛ زیرمنوی Pool در
  اسکرین‌شات دیده می‌شود.
- radio/checkmark و تغییر برنامه‌ای: radio در submenu رندر می‌شود و تغییر state در
  منوی باز با `ItemsPropertiesUpdated` اعمال شد؛ checkmark در layout و host تأیید شد
  ولی اسکرین‌شات آن گرفته نشد.
- visible/enabled: `SetProps` با `ItemsPropertiesUpdated` به host می‌رسد و layout
  مقادیر `visible=false`/`enabled=false` را نشان می‌دهد؛ رندر پنهان/خاکستری به‌صورت
  تصویری جدا ثبت نشد.
- add/remove/relabel در زمان اجرا: **در منوی باز** اعمال می‌شوند. اسکرین‌شات‌ها
  `Live relabel #1` و ردیف `Job: pool refresh running` را در همان منوی باز نشان
  می‌دهند؛ برای add، host حدود ۱۳٫۳ms بعد از درخواست apply (و ≈۰٫۳ms بعد از
  `LayoutUpdated`؛ timestamps خام در `menu-kde-2026-11-25.txt`) یک `GetLayout(0,1)`
  می‌فرستد و منو بسته نمی‌شود. بنابراین R15-D1 به fallback «فقط روی بازشدن بعدی» نیاز ندارد.
- صفحهٔ ۲۰آیتمی از fixture ۱۷۶ (۱۲۰/۲۴/۱۲/۲۰)، صفحه ۲/۶ و clamp ۹۹→۶: کار می‌کند.
  label بلند ۱۳۱ rune با فارسی/یونانی/لاتین و label چندزبانهٔ بدون ESC در D-Bus
  دست‌نخورده می‌مانند. هر `LayoutUpdated` یک `GetLayout` جدا از host می‌گیرد: یک
  صفحه‌گردانی ۲۰ردیفی ۵۱ فراخوانی (≈۶ms) ساخت؛ D1 باید rebuild را دسته‌بندی کند یا
  این هزینهٔ محلی را بپذیرد.
- eventها: `AboutToShow(0)` سپس `Event(0,"opened")` و `GetLayout(0,1)` در باز شدن؛
  برای زیرمنو `AboutToShow(id)`/`Event(id,"opened")`/`GetLayout(id,1)` (idهای ۴۵ و
  ۵۰ دیده شد)؛ `Event(id,"closed")` در بستن. host در این اجراها `Event clicked` نساخت چون هیچ
  action منو کلیک نشد؛ مسیر clicked به‌صورت مصنوعی آزموده شد و handler آن ۸۵µs بود
  (اثر `SetProps` همان `ItemsPropertiesUpdated` است). rate limit برای dbusmenu دیده نشد (محدودیت notification در R13-A همچنان مربوط است).
- واقعیت کتابخانه: `MenuItem` شناسهٔ عمومی ندارد؛ radio group خودکار نیست؛ حذف آخرین
  فرزند `children-display: ''` باقی می‌گذارد. D1 باید key پایدار خودش را نگه دارد،
  state همهٔ radioها را خودش set کند و برای submenu خالی placeholder بگذارد.

**محدودیت‌ها/آزمون‌نشده:** تأیید بصری انسانی و اسکرین‌شات checkmark و صفحهٔ سطح سوم
گرفته نشد (host زیرمنو را پیش از capture بست، هرچند `GetLayout` آن ثبت شد)؛ یک اجرای
بعدی هنگام fullscreen بودن Jitsi پنل در اسکرین‌شات دیده نشد و آیکن پیدا نشد؛ آن اجرا
برای هیچ verdict استفاده نشد؛ Wayland/GNOME و سایر DEها آزمون نشدند؛ `riscv64`/`ppc64le` فقط
cross-build هستند؛ رندر label بلند و truncation بصری اندازه‌گیری نشد؛ actionهای
واقعی/restart/notification در دامنهٔ R15-B..G هستند. verdict R15-A برای شروع
R15-B **Ready** است، با همین شکاف‌های ثبت‌شده.
