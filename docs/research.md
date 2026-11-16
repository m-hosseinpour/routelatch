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

Xray `v26.11.9` در [release metadata](https://api.github.com/repos/XTLS/Xray-core/releases/tags/v26.11.9)
با تاریخ انتشار ۲۰۲۶-۱۱-۰۹ مشاهده شد. [go.mod آن](https://raw.githubusercontent.com/XTLS/Xray-core/v26.11.9/go.mod)
`go 1.27` و مجموعهٔ وابستگی‌ها را نشان می‌دهد؛ ساخت واقعی انجام نشده است. این نسخه
مرجع تحقیق است، نه نسخهٔ الزام‌آور release RouteLatch. انتخاب نسخه و toolchain در T1.

## مقایسهٔ گزینه‌های عمومی

| گزینه | شاهد خوانده‌شده | جمع‌بندی محدود |
| --- | --- | --- |
| Xray | E01–E10 و [XHTTP reference](https://github.com/XTLS/Xray-core/discussions/4113) | سازگاری transport و مسیر Go مناسب‌تر برای نیاز فعلی؛ نیازمند frontend/adapter و آزمون |
| sing-box | [transportها](https://sing-box.sagernet.org/configuration/shared/v2ray-transport/)، [URLTest](https://sing-box.sagernet.org/configuration/outbound/urltest/)، [mixed](https://sing-box.sagernet.org/configuration/inbound/mixed/)، [API](https://sing-box.sagernet.org/configuration/service/api) | TUN/DNS/انتخاب آماده؛ فهرست رسمی transportها هم‌ارزی کامل XHTTP را اثبات نمی‌کند؛ API مستند mutation معادل Xray در بررسی یافت نشد، نه اثبات نبود همهٔ مسیرهای داخلی |
| mihomo | [fallback](https://wiki.metacubex.one/en/config/proxy-groups/fallback)، [URL-test](https://wiki.metacubex.one/en/config/proxy-groups/url-test)، [COMPATIBLE](https://wiki.metacubex.one/en/config/proxies/built-in/) و [group config](https://wiki.metacubex.one/en/config/proxy-groups) | الگوی سلامت/انتخاب؛ COMPATIBLE معادل DIRECT است و empty-group policy باید کنترل شود |
| GOST | [Bypass](https://gost.run/en/concepts/bypass) و [Load balancing](https://gost.run/en/concepts/load-balancing) | الگوی جداسازی سیاست، انتخاب و hot reload؛ هم‌ارزی transport pool اثبات نشده |
| COW | [README](https://github.com/cyfdecyf/cow/blob/master/README-en.md) | مستقیم اول، retry، سابقه، استثنا و PAC؛ ایدهٔ مشابه، نه موتور انتخاب‌شده یا پروژهٔ امنیت‌سنجی‌شده |
| OpenTUI | [README](https://github.com/anomalyco/opentui): Zig و TypeScript/React/Solid | برای TUI جدی نامزد است؛ با تصمیم منوی سبک نیازی به runtime جدا نداریم |
| huh | [README](https://github.com/charmbracelet/huh): forms/prompts، standalone و accessible mode | نامزد منوی سبک Go؛ هنوز انتخاب/آزمون نشده |
| goproxy | [README](https://github.com/elazarl/goproxy)، [proxy.go](https://raw.githubusercontent.com/elazarl/goproxy/master/proxy.go)، [https.go](https://raw.githubusercontent.com/elazarl/goproxy/master/https.go): CONNECT، custom transport/dial، hijack | نامزد frontend؛ MITM اختیاری باید خاموش باشد؛ fit و retry/stream semantics آزمون می‌خواهد |

## سیستم‌عامل و service manager

[sing-box client guide](https://sing-box.sagernet.org/manual/proxy/client) فرق system
proxy، firewall redirect و virtual interface را توضیح می‌دهد. پروکسی دسکتاپ همهٔ TCP
یا UDP را اجباراً capture نمی‌کند.

[Windows SCM](https://learn.microsoft.com/en-us/windows/win32/services/service-control-manager)
مدیریت service بومی را تعریف می‌کند و [WinHTTP/WinINet](https://learn.microsoft.com/en-us/windows/win32/winhttp/setting-wininet-proxy-configurations-in-winhttp)
تفاوت استفادهٔ برنامه‌ها از تنظیمات proxy را نشان می‌دهد. طرح: core قابل حمل، lifecycle
بومی. هیچ Windows runtime check، Android build یا آزمایش systemd hardening انجام نشد.

## فرض‌های اصلاح‌شده و پوشش باقی‌مانده

- فرض «Xray برای TUN موتور دیگری لازم دارد» با شواهد جدید اصلاح شد؛ Android همچنان
  اپ بومی می‌خواهد و رفتار session registry آن باز است.
- فرض «subprocess + API همهٔ مدیریت اتصال را حل می‌کند» اصلاح شد؛ E03–E06 مسیر
  استفادهٔ کتابخانه‌ای/frontend را ضروری یا نیازمندی ساده‌تر را محتاج تأیید می‌کند.
- گزینهٔ موقت «همهٔ IPهای نامشخص pool» با تصمیم D10 جایگزین شد.
- منابع زندهٔ main به‌عنوان سند release قطعی تلقی نشوند؛ در T1 نسخه pin و شواهد بسته شود.
- آزمون‌های throughput/پایداری، handshake و error classification واقعی، license audit
  وابستگی‌ها، امنیت frontend و تازه‌بودن قالب دیتابیس v2rayN هنوز انجام نشده‌اند.
- بررسی نام `RouteLatch` فقط یک جست‌وجوی اولیه بود؛ برخورد واضح پروژهٔ proxy در
  نتایج مشاهده نشد. تضمین یکتایی، availability رجیستری یا علامت تجاری نیست.
