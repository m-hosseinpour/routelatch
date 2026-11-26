# معماری و امنیت

طرح ۲۰۲۶-۱۱-۱۷. تصمیم‌های محصول در [نیازمندی‌ها](requirements.md) مرجع‌اند؛
قراردادهای اجرایی، اعداد و ماتریس‌ها در [قراردادها](contracts.md) هستند و این سند
فقط مرز اجزا و «چرا» را نگه می‌دارد. نسخه‌های pin و شاهدهای `T1` در
[تحقیق](research.md) و `spike/*/FINDINGS.md` ثبت شده‌اند.

## ۱. مرز مسئولیت‌ها

```text
CLI / منوی huh / tray control surface / GUI آینده
            │ management Unix socket + JSON (0600، SO_PEERCRED)
            │ (tray فقط برای quit/restart پیام lifecycle به supervisor می‌دهد)
            ▼
سرویس Go ── policy/state ── pool و probe ── پیشنهادها و شواهد
    │            ▲
    │        internal/contract  (تایپ‌ها و اینترفیس‌های مشترک، بدون منطق)
    │
    ├── HTTP/CONNECT با net/http و SOCKS5 با go-socks5
    │       └── registry: مقصد، مسیر، PolicyVersion، cancel/close
    │
    ├── مقصد مستقیم ── dialer مستقیم و DNS سیستم ── شبکه
    └── مقصد pool ── adapter هستهٔ Xray pin‌شده ── tag صریح ── خروجی ── مقصد
```

کد اختصاصی ما: سیاست، وضعیت، انتخاب خروجی، ثبت/قطع اتصال، sanitization و CLI.
پارس HTTP/SOCKS، VLESS/Trojan/XHTTP و رمزنگاری از کتابخانه‌های آماده می‌آیند.
یک مالک state وجود دارد؛ CLI وقتی سرویس بالاست فایل را مستقیم نمی‌نویسد.

## ۲. اتصال کتابخانه‌ای و محدودیت‌های موتور

**کلاس اثبات، نه «Dial موفق».** `core.Dial` در ~۲۳µs با `nil` برمی‌گردد، در حالی که
مقصد ~۵۰۲ms بعد لمس می‌شود؛ با خروجی مرده هم همان `nil` و صفر تماس با مقصد دیده شد.
پس دو کلاس داریم: `ProofConfirmed` (reply خود خروجی SOCKS5/HTTP موفقیت را گزارش
می‌کند؛ assertion خروجی، نه تأیید مستقل) و `ProofAccepted` (VLESS/Trojan: فقط dispatch
پذیرفته شد). `2xx`/`0x00`
برای کلاس دوم هم فرستاده می‌شود، ولی همه‌جا «accepted، مقصد تأیید نشده» نامیده
می‌شود و هرگز «مقصد در دسترس است». نبود خروجی سالم، خطای روشن پیش از reply است و
هیچ fallback مستقیمی وجود ندارد (D11). جزئیات و جدول reply: `contracts.md §۲`.

**انتخاب خروجی در Go است، نه balancer هسته.** در T1 یک balancer خالی/بی‌تطبیق به
default handler رسید و direct شد (۱ تماس با مقصد). هر dial هسته یک tag صریح دارد و tag
غایب/مرده خطای typed می‌دهد؛ exitهای SOCKS5/HTTP را adapter خودمان (برای داشتن
`ProofConfirmed`) dial می‌کند و انتخابشان با exit id صریح است. outbound پیش‌فرض
کانفیگ هرگز direct نیست.

**`PoolDialDeadline` مرز dial نیست، مرز adapter است.** این عدد فقط handshake مسدود
adapter SOCKS5 را واقعاً می‌بندد؛ مسیر core پیش از تماس با خروجی برمی‌گردد، پس فاز dial
قابل مشاهده ندارد و یک session گیرکردهٔ core در مرحلهٔ تونل به `timeout` یا
`destination_unconfirmed` تبدیل می‌شود، نه به یک dial deadline.

**کتابخانه‌ها.** `goproxy v1.9.4` رد شد: پاسخ را بازنویسی می‌کند (reason/header/chunk)،
stream غیر-SSE را بافر می‌کند، بایت‌های pipelined CONNECT را گم می‌کند و یک GET را
روی اتصال reused بازپخش می‌کند. HTTP را با `net/http` خودمان می‌نویسیم: hijack،
dial، سپس `200`، سپس replay بایت‌های buffer. برای CONNECT مسیر pool، `200` از dial
موفق جدا شده و «تعهد» به اولین بایت مقصد بسته است: بایت‌های کلاینت تا سقف محدود
بافر و به exit جاری فرستاده می‌شوند و مرگ پیش از بایت اول با همان بایت‌ها روی exit
دیگری retry می‌شود (بدون ۲۰۰ دوم و بدون fallback مستقیم؛ `contracts.md §۲.۲`). SOCKS5 با
`go-socks5 v0.1.4` فقط با resolver بدون lookup (hostname تا خروجی حفظ می‌شود) و
`WithDialAndRequest`؛ BIND و UDP ASSOCIATE صریحاً `0x07` می‌گیرند و هیچ سوکت UDP باز نمی‌شود.
UDP در v1 رد است.

**چرخهٔ عمر موتور.** یک `core.Instance` در هر process (probe هم از همان با tag صریح)؛
restart درون‌process ممنوع است، چون `core.Stop` وضعیت dialer سراسری gRPC/XHTTP را پاک
نمی‌کند (۲→۵ goroutine و transportهای warm باقی می‌مانند). tag هر exit پایدار است
(`rl-<recordID>`) و با تغییر mode/rule عوض نمی‌شود؛ تصمیم مقصد در registry است و
`PolicyVersion` فقط dialهای کهنه/exit حذف‌شده را fence می‌کند. حالت «متوقف» engine را می‌بندد؛ خروج از
آن process جدید می‌خواهد. سوکت‌های idle باقی‌مانده در `status` شمرده می‌شوند و نقض
D13 نیستند، چون هیچ درخواست جدیدی از آن‌ها استفاده نمی‌کند. انتخاب عادی exit، موتور را
restart نمی‌کند.

## ۳. سیاست مسیر و DNS

- یک مدل مقصد نوع‌دار و یک فهرست مشترک (D10): دامنهٔ دقیق، زیردامنهٔ انتخابی، IP و
  CIDR؛ بدون wildcard و بدون public suffix. نرمال‌سازی و جدول ورودی‌های مرزی:
  `contracts.md §۱`.
- اولویت کلاس قبل از قانون: Self → Local → LAN → Approved → Other. loopback،
  link-local و LAN همیشه direct هستند (D17) و هیچ قانونی آن‌ها را pool نمی‌کند؛
  Self فقط با پورت‌های listener پیکربندی‌شده (`routing.Config.SelfPorts`) شناخته
  می‌شود و loopback روی پورت دیگر Local است. تصمیم Self `policy_denied` گزارش
  می‌شود تا loop به listener خودمان پنهان نماند.
- در targeted فقط تطبیق صریح pool است؛ IP نامشخص خارج از فهرست direct می‌ماند.
  hostname با قانون IP/CIDR مطابقت داده نمی‌شود، چون resolve نمی‌کنیم.
- remote hostname تا خروجی حفظ می‌شود (D15)؛ مسیر مستقیم از resolver سیستم استفاده
  می‌کند. اگر مصرف‌کننده خودش hostname را resolve کند، تصمیم روی IP گرفته می‌شود و
  محدودیت در `status`/docs گفته می‌شود. reverse DNS، IP مشترک CDN و sniffing (خصوصاً
  با ECH) اثبات دامنه نیستند.
- IPv4-mapped IPv6، IP literal و پورت‌ها یکسان‌سازی می‌شوند؛ نبود پشتیبانی یک مسیر
  هرگز bypass مستقیم نمی‌سازد.
- در مدل پیش‌فرض DNS سیستم، firewall و TUN تغییر نمی‌کنند. پوشش TUN (D29، §۹)
  opt-in است و DNS سیستم و firewall را تغییر نمی‌دهد؛ فقط routeهای TUN و یک
  سرویس کمکی جدا اضافه می‌شوند.

## ۴. pool و سلامت

منبع → رکورد canonical → وضعیت اعتبار/اعتماد → شواهد سلامت → انتخاب خروجی. هویت از
تنظیمات اتصال می‌آید (hash)، نه عنوان یا منبع؛ یک لینک مشترک در دو اشتراک یک رکورد با
دو منبع است. وضعیت‌ها: invalid/unsupported/unknown/healthy/cooling؛ رکورد
skip-verify با دلیل `insecure_tls_removed` در `unsupported` می‌ماند.

- import فقط allow-list می‌سازد؛ root routing/dns/env/api/inbounds، script، مسیر فایل
  محلی و outbound dependency ناشناس دور ریخته یا رکورد رد می‌شود. XHTTP extra فقط
  به‌عنوان دادهٔ allow-listed می‌ماند. موفقیت config builder شاهد sandbox نیست.
- refresh فقط با فرمان صریح؛ fetch محدود (۴MiB، ۳ redirect، ۳۰s) و commit اتمیک.
  خطای refresh/HTML/صفر رکورد، pool سالم قبلی را پاک نمی‌کند.
- سلامت: transport probe (فقط endpoint خروجی) به‌صورت خودکار در targeted/all-proxy؛
  egress فقط با هدف صریح کاربر. شواهد مثبت = بایت برگشتی؛ خطای مقصد در VLESS/Trojan
  قابل انتساب نیست و رکورد را cooling نمی‌کند. در all-direct/stopped هیچ probe خودکار
  و هیچ مقایسهٔ پنهان مستقیم/پروکسی وجود ندارد (D09). تاریخ نمونه‌ها ring حافظه‌ای
  محدود است و فقط خلاصهٔ `ExitHealth` persist می‌شود؛ پس از restart سلامت از probeها
  دوباره یاد گرفته می‌شود. اعداد رتبه/cooldown: `contracts.md §۶`.
- v2rayN فقط به‌صورت فایل export‌شده و read-only؛ `guiNDB.db` در v1 خوانده نمی‌شود.

## ۵. پیشنهادها و حریم خصوصی

شکست مستقیم واجد شرایط → probe محدود مستقل → پیشنهاد با دلیل و تازگی → تأیید/رد/بررسی
دوباره. درخواست شکست‌خورده بازپخش نمی‌شود و پیشنهاد هرگز خودش قانون نمی‌سازد.

اهلیت فقط از سه شاهد مستقیم می‌آید: refused، timeout و DNS failure. HTTP status،
CAPTCHA، rate-limit، گواهی نامعتبر، لغو، تغییر حالت، EOF و هر خطای مسیر pool شاهد
نیستند. این جدول در `contracts.md §۳.۵` است و `ProbeResult.Eligible` تنها دروازهٔ
ساخت پیشنهاد است.

evidence فقط مقصد/پورت/نوع/زمان/نتیجه را نگه می‌دارد، نه URL، query، header، body،
کوکی یا token. retention محدود و پاک‌کردن صریح دارد؛ خاموش‌کردن تشخیص صف را لغو
می‌کند و probe مقصد جدید را متوقف می‌کند، ولی سلامت pool و فرمان دستی مستقل‌اند.

## ۶. امنیت محلی و ورودی‌ها

| تهدید | کنترل | محدودیت باقی‌مانده |
| --- | --- | --- |
| استفادهٔ کاربر محلی دیگر از pool | loopback و هشدار حالت بدون رمز | طبق D14 احراز هویت traffic نداریم |
| تغییر mode/rules توسط فرایند دیگر | Unix socket `0600` در پوشهٔ `0700`، `SO_PEERCRED` با uid برابر، بدون HTTP management | هم‌uid و root خارج از مرز |
| API داخلی موتور | API عمومی فعال نمی‌شود؛ اگر لازم شد socket خصوصی | loopback بی‌auth «امن» نیست |
| خروجی عمومی مخرب | بدون MITM، TLS مقصد، برچسب اعتماد و دلیل `unsupported` برای TLS ناامن | exit metadata و HTTP plaintext مقصد را می‌بیند |
| subscription/share link مخرب | allow-list، sanitization، محدودیت اندازه/منبع، پیام redacted | builder سندباکس نیست؛ اجرا نمی‌کنیم، فقط داده می‌سازیم |
| secret روی دیسک/ترمینال | پوشه `0700`، فایل `0600`، atomic write، redaction، حذف control/escape/bidi از هر رشتهٔ پویا | ویندوز ACL جدا می‌خواهد |
| فرایند/فایل نامرتبط | singleton با flock، تصادم پورت خطای روشن، بدون kill/delete گسترده | — |
| loop به listener/مدیریت | ClassSelf، refuse قبل از dial، بدون تنظیم proxy دسکتاپ در خطا | — |

TLS ناامن (D18): هستهٔ pin‌شده `allowInsecure` را حذف کرده و رضایت نمی‌تواند رکورد را
قابل dial کند؛ پس در v1 هیچ واحد/فرمان/فیلد رضایتی وجود ندارد. رکورد skip-verify با
دلیل پایدار `insecure_tls_removed` و hint در `pool list`/`status`/`doctor` دیده
می‌شود و هرگز dial نمی‌شود. `pinnedPeerCertSha256`/`verifyPeerCertByName` با verification
روشن پذیرفته می‌شوند؛ این pin است، نه استثنای ناامن. بازگشت مسیر رضایت فقط با استثنای
محدود، قابل‌مشاهده و قابل‌لغو ممکن است (`contracts.md §۹` R-14). `huh` خودش sanitize
نمی‌کند (اندازه‌گیری شد)، بنابراین همهٔ رشته‌های پویا از یک `SanitizeTerminal` می‌گذرند.

## ۷. سرویس، پلتفرم و استفاده

> **وضعیت:** این بخش مدل اجرای تصمیم‌شده در D26 (۲۰۲۶-۱۱-۲۳) را توصیف می‌کند.
> R13 در درخت کامل شده است (R13-F verdict **Ready**) و `v1.0.0` منتشر شده
> است؛ بسته‌های `v0.1.3` و پیش از آن همان unit و `run` foreground را دارند.
> README/INSTALL/SERVICE در R13-E به‌روز شدند. R15 (D28) در همین بخش طراحی
> شده است؛ R15-A..C (اسپایک منو، controller/model و restart سطح process)،
> اتصال واقعی tray در R15-D2 و دامنه‌های R15-E..G2 (pool، rules/proposals،
> desktop/diagnostics/data/settings و Terminal menu/Quit) پیاده شده‌اند و منوی controller
> جای سه آیتم ثابت را گرفته است. پذیرش R15-H در ۲۰۲۶-۱۱-۲۶ روی data-dir ایزوله
> اجرا و شاهدش در `spike/tray/evidence/r15h-acceptance-2026-11-26.md` ثبت شد؛
> بازبینی مستقل، commit و تصمیم انتشار مراحل جدا هستند.

- Linux `amd64`+`arm64`؛ در مدل پیش‌فرض root/TUN لازم نیست. پوشش TUN اختیاری
  (D29) یک helper ممتاز یک‌باره می‌خواهد، ولی engine همچنان بدون root و با
  همان management/socket model اجرا می‌شود. `routelatch run` پیش‌فرض اپ است: از
  ترمینال detach می‌شود، tray (و بعداً GUI) نشان می‌دهد و engine را به‌عنوان child
  `routelatch run --headless` اجرا و supervise می‌کند. `--headless` تنها حالت
  جایگزین است: engine foreground بدون tray/GUI برای ترمینال، SSH، container، تست و
  systemd. فرمان جدا برای `tray`/`gui` وجود ندارد؛ اگر GUI بعداً اضافه شود، همان
  `run` پیش‌فرض GUI+tray می‌شود و `--headless` دست‌نخورده می‌ماند (D26). حالت
  اپ برای teardown به `pidfd_open` مجاز نیاز دارد (Linux ≥5.3 و مسدودنشده با
  seccomp)؛ در غیر این صورت `unsupported`/`7` با دلیل `pidfd_unavailable` و
  پیشنهاد `--headless`/سرویس.
- launcher با `setsid`/readiness از ترمینال جدا می‌شود و فقط وقتی برمی‌گردد که
  management socket با `status.ready=true` پاسخ دهد و tray ثبت شده باشد؛
  listenerها فقط در mode غیر `stopped` برای readiness لازم‌اند. در timeout،
  launcher با pidfdهای `(pid,starttime)` تأییدشده سیگنال می‌فرستد (تنها استثنا:
  `SIGKILL` به supervisor مستقیم/reap‌نشده اگر pidfd آن باز نشود؛ تا `waitpid`
  PID قابل reuse نیست)، مرگ supervisor را با `waitpid` و مرگ هر فرزند
  گزارش‌شده را با pidfd/`/proc` تا deadline تأیید می‌کند، سپس با pidfd
  supervisor باز گروه قدیمی را اسکن می‌کند؛
  عضو زنده/تأییدنشده یعنی `teardown_incomplete`/exit `1`، نه `8`. exit `8` یعنی
  «همهٔ processهای گزارش‌شده و اعضای تأییدشدهٔ همان invocation دیگر زنده
  نیستند»، نه آزادبودن global lock (یک `run` هم‌زمان می‌تواند پس از آن app جدید
  بسازد). خطای
  startup با exit code به فراخواننده می‌رسد. engine child state lock و management
  socket قبل را دارد؛ invariant دقیق «یک engine در هر data directory» است، نه هر
  کاربر. همهٔ lock/socket/app files از یک `InstanceDir` مشتق از state/data
  directory (`<StateDir>/run` یا `<data-dir>/run`) می‌آیند، نه از
  `XDG_RUNTIME_DIR`؛ بنابراین desktop و SSH با runtime env متفاوت همان lock و
  socket را می‌بینند. در پنجرهٔ migration، engine lockهای legacy **همان data
  directory** را probe/نگه می‌دارد (برای `--data-dir` فقط lock همان ریشه، نه
  runtime سراسری) تا engine قدیمی حتی در mode `stopped` دیده شود و instanceهای
  مستقل قربانی نشوند. app-mode یک `flock` روی `InstanceDir/app.lock` (singleton)
  و یک `flock` دوم روی `InstanceDir/app.owner` (readiness/owner) دارد که
  supervisor در عملیات عادی تا خروج نگه می‌دارد؛ در پنجرهٔ fail-closed،
  `app.owner` اول آزاد می‌شود و `app.lock` تا پایان توقف child/tray می‌ماند.
  گرفتن موفق `app.owner` به‌تنهایی هرگز یعنی supervisorی نیست؛ `app.lock` اول
  بررسی می‌شود. marker (`starting`/`ready`/`stopping`، `generation`، `pid`،
  `data_dir`) دادهٔ `already_running` است و فقط وقتی `ready` معتبر است که flock
  آن در اختیار supervisor زنده باشد؛ liveness را lock تعیین می‌کند، نه PID. readiness
  event-driven است: پیش از restart/Quit/tray-loss، `starting`/`stopping` نوشته
  می‌شود و `ready` فقط پس از `status.ready` کودک جدید و tray؛ `run` جدید علاوه
  بر marker، با یک negative check از socket مطمئن می‌شود `ready` کهنه نیست
  (socket هرگز به‌تنهایی `0` نمی‌سازد). supervisor در عملیات عادی مالک lockها
  است؛ در پنجرهٔ fail-closed `app.owner` اول آزاد می‌شود و `app.lock` تا توقف
  child/tray می‌ماند و پیش از خروج آزاد می‌شود. engine child lockها را با
  `CLOEXEC` به ارث نمی‌برد. Quit یا مرگ supervisor باید child را متوقف/reap کند (parent-death یا
  watchdog) تا listener/management یتیم نماند. restart فقط با جایگزینی process انجام
  می‌شود (tray child، supervisor به‌درخواست tray، یا systemd)؛ restart
  درون‌process ممنوع می‌ماند (R-4). بستن
  ترمینال پس از readiness پروسهٔ detached را متوقف نمی‌کند.
- tray با StatusNotifierItem/D-Bus و کتابخانهٔ pure Go هدف‌گذاری می‌شود تا
  `CGO_ENABLED=0` حفظ شود. GNOME بدون extension و SSH/بدون display tray ندارند؛
  `run` پیش‌فرض در این محیط‌ها با پیام روشن fail می‌شود و `--headless` پیشنهاد
  می‌دهد؛ fallback بی‌صدا وجود ندارد. KDE مسیر آزمون اولیه است؛ سایر DEها تا آزمون
  واقعی ادعا نمی‌شوند.
- **سطح کنترل tray (D28؛ controller/model در R15-B و اتصال واقعی deedles در R15-D2 پیاده شدند).** tray child یک کلاینت نازک
  روی همان management socket است، نه یک لایهٔ منطق دوم:
  status/mode/rules/pool/proposals/desktop/backup/doctor
  را با همان `control.Request`های موجود می‌خواند و تغییر می‌دهد. فقط `quit` و
  `restart-engine` از pipe به supervisor می‌روند، چون lifecycle اپ را engine
  مدیریت نمی‌کند. tray هیچ فایل state را مستقیم نمی‌خواند/نمی‌نویسد، listener
  جدید باز نمی‌کند و سرویس/systemd را مدیریت نمی‌کند. منطق منو در یک بستهٔ pure-Go
  و testable (`internal/trayui`، مسیر پیشنهادی) از binding D-Bus جدا می‌شود؛
  GUI آینده می‌تواند از همان management contract استفاده کند، نه از داخلی‌های tray.
  actionهای طولانی asynchronous‌اند و handler D-Bus را بلوکه نمی‌کنند؛ عمل مخرب
  با یک pending confirmation دو مرحله‌ای اجرا می‌شود. labelها از همان
  `SanitizeTerminal` می‌گذرند و notification/log هیچ مقصد/URL/credentialی ندارد
  (D21). عملیات نیازمند متن در CLI/`Terminal menu` می‌مانند. جزئیات درخت منو،
  refresh و حالت‌ها در `contracts.md §۷.۳` است. این تصمیم GUI کامل نیست (D24).
- **restart سطح process.** درخواست `Restart engine` از tray فقط یک پیام
  `restart-engine` به supervisor است. supervisor پیش از توقف child `starting`
  می‌نویسد، engine را با همان مسیر `run --headless` جایگزین می‌کند و `ready` را
  فقط پس از `status.ready` کودک جدید و ثبت tray می‌نویسد؛ شکست مانند مسیرهای
  fail-closed موجود متوقف می‌شود. restart درون‌process و تغییر mode در supervisor
  ممنوع می‌ماند. restart دستی از سقف restart خودکار خرابی مصرف نمی‌کند و tray
  درخواست‌ها را سریال می‌کند. حالت stopped پس از restart در process تازه با
  انتخاب mode قابل ترک است؛ tray این انتقال را صریح نشان می‌دهد و actionهای
  نامعتبر را غیرفعال می‌کند.
- شروع در login با XDG autostart است، نه unit: `routelatch autostart enable` یک فایل
  `$XDG_CONFIG_HOME/autostart/routelatch.desktop` (fallback `~/.config/autostart`)
  با `Exec=<absolute> run` می‌سازد؛ پیش از نوشتن/اعلام idempotent، tray host و
  pidfd مجاز را probe می‌کند و در نبود هرکدام `unsupported`/`7` با دلیل
  `tray_unavailable`/`pidfd_unavailable` می‌دهد. autostart فقط instance پیش‌فرض
  است و هر سه فرمان با `--data-dir` رد می‌شوند؛ برای data-dir سفارشی
  `service install --data-dir` مسیر درست است. نصب بسته autostart نمی‌سازد؛
  linger/start-before-login بخشی از مدل دسکتاپ نیست.
- برای VPS/server یک سرویس opt-in وجود دارد: `routelatch service install` یک systemd
  **user unit** در base config همان user manager (از `systemctl --user
  show-environment`، fallback `~/.config/systemd/user`) با
  `ExecStart=<absolute> [--data-dir <abs>] run --headless` می‌سازد، baseهای
  مطلق config/state را در unit pin می‌کند، enable/start می‌کند و فقط وقتی
  `status.ready` واقعی شد (نه فقط `ActiveState`) موفقیت اعلام می‌کند؛ در شکست
  محتوای unit و enabled/active قبلی را در یک rollback برمی‌گرداند. اگر linger
  خاموش باشد فقط `sudo loginctl enable-linger <user>` را چاپ می‌کند؛ خودش sudo
  نمی‌زند. unit با یک marker مالکیت مشخص می‌شود؛ uninstall اول ownership را
  می‌سنجد و فقط unit مالک را حذف می‌کند. `autostart` و `service` برای یک کاربر
  هم‌زمان فعال نمی‌شوند و install هر کدام دیگری را detect و refuse می‌کند. unit
  نصب‌شده بخشی از بسته نیست؛ بسته‌های منتشرشده تا `v0.1.3` unit قدیمی دارند و
  از R13-E به بعد هیچ unit/autostart/postinst در بسته نیست. جزئیات در
  `docs/contracts.md §۷.۵` است. **استثنای D29/R16:** `tun setup` یک system unit
  جدا برای سرویس کوچک TUN می‌سازد (فقط با `CAP_NET_ADMIN`، بدون traffic listener)؛
  این unit بخشی از بسته نیست و engine/tray را root نمی‌کند.
- آرتیفکتهای `riscv64` و `ppc64le` فقط cross-build و runtime-untestedاند؛ پشتیبانی runtime نسخهٔ v1 همان amd64/arm64 است. بستهٔ `.deb` محلی محتوای tarball را به `/usr` نگاشت میکند (`bin`→`/usr/bin`، `share/doc`→`/usr/share/doc`، `share/routelatch`→`/usr/share/routelatch`)، بسته‌های منتشرشده تا `v0.1.3` unit دارند و از `v1.0.0` طبق D26 هیچ unit/postinst یا فعالسازی سرویسی ندارد؛ sidecar فقط برای amd64/arm64 قرار میگیرد و `.deb` پاورپیسی با نام Debian یعنی `ppc64el` ساخته میشود. اسپک RPM برای هر چهار معماری (`amd64`→`x86_64`، `arm64`→`aarch64`، `riscv64` و `ppc64le` بدون تغییر) از همان BUILD-INFO ساخته و در SHA256SUMS ثبت میشود؛ VERSION آن دقیقاً برابر `.deb` است. LICENSE/NOTICE زیر `/usr/share/licenses/routelatch` و بقیهٔ اسناد زیر `/usr/share/doc/routelatch` نصب میشوند، بدون scriptlet یا فعالسازی سرویس. rpmbuild روی host نصب نیست؛ باینری RPM در container یک‌بارمصرف ساخته، بازرسی و payload آن اجرا شده است، CI نیز باینری‌ها را می‌سازد/بازرسی می‌کند؛ انتشار `v1.0.0` با مجوز کاربر انجام شد. نصب واقعی تحت RPM و اجرای سرویس opt-in روی معماری‌های غیر amd64 هنوز آزمون‌نشده‌اند.
- پروکسی دسکتاپ opt-in است و فقط GNOME (`gsettings`) و KDE (`kwriteconfig`) را
  پشتیبانی می‌کند؛ بقیهٔ محیط‌ها پیام صریح «پشتیبانی نمی‌شود» می‌گیرند، نه ادعای
  «پروکسی کل سیستم». شکست خود ابزار `external_tool_failed` (exit `1`) است و نتیجهٔ
  تنظیم نامعلوم اعلام می‌شود؛ مقدار قبلی حفظ و فقط تنظیمی که خودمان نوشتیم restore
  می‌شود؛ `desktop restore` آفلاین هم کار می‌کند.
- launcher فقط در فرزند خودش `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY`/`NO_PROXY` را
  تنظیم می‌کند؛ `NO_PROXY` ارث‌رسیده قانون تأییدشده را دور نمی‌زند. توقف یا خرابی
  listener مجوز اجرای مستقیم نیست. برنامه‌ای که مستقل اجرا شده خارج از کنترل ماست،
  همان‌طور که DNS آن خارج از listener است.
- crash می‌تواند اشارهٔ دسکتاپ به listener مرده بگذارد؛ `doctor` و `desktop restore`
  مسیر بازیابی‌اند و هرگز bypass پنهانی نمی‌سازند.

## ۸. مراحل بعدی

Windows به named-pipe/ACL و SCM نیاز دارد. Android `VpnService` بومی و یک Go runtime
در artifact می‌خواهد. GUI بعداً همان `run` پیش‌فرض را به GUI+tray تبدیل می‌کند و
`--headless` دست‌نخورده می‌ماند؛ فرمان `gui` جدا وجود ندارد. انتخاب framework
سرمایه‌گذاری جدید و خارج از v1 است (D24/D26).

TUN در D29 (۲۰۲۶-۱۱-۲۶) از «مرحلهٔ بعدی مبهم» به یک milestone پس از v1 (R16) تبدیل
شد و طراحی آن در §۹ آمده است. UDP از pool همچنان تصمیم/پیاده‌سازی جدا می‌خواهد؛
فاز اول TUN فقط UDP غیرDNS را بلاک می‌کند. در R16-C سرویس جداگانهٔ TUN
(`internal/tun` و `__tun-service`) با lifecycle، lease، control socket و seam
interface فیزیکی پیاده شده است؛ اتصال آن به engine (R16-D) و unit/socket نصب‌شده
(R16-G) هنوز پیاده نشده‌اند و E/F باز است. بنابراین TUN هنوز end-to-end اجرا یا
پشتیبانی نمی‌شود و ادعای پشتیبانی آن در v1.x وجود ندارد.

## ۹. پوشش TUN (D29؛ تعریف ۲۰۲۶-۱۱-۲۶؛ R16-C: سرویس و lifecycle پیاده، اتصال engine و نصب باز)

**مرز.** TUN یک **حالت پنجم نیست**؛ یک لایهٔ پوشش روی `targeted`/`all-proxy`/
`all-direct` است. جدول تصمیم §۳، فهرست تأییدشده، انتخاب exit، registry، D11 و
D13 تغییر نمی‌کنند. TUN فقط ترافیک برنامه‌هایی را به همان listenerهای محلی
می‌رساند که پروکسی سیستم را نادیده می‌گیرند. `stopped` همچنان «بدون listener» است
و TUN در آن فعال نمی‌شود.

```text
برنامهٔ بدون پروکسی
        │ packet (IPv4/IPv6)
        ▼
   TUN (rl-tun0) ── Xray TUN inbound + sniffing ──┐
        │  TCP (domain اگر TLS/HTTP قابل sniff باشد)│
        └──────────────► SOCKS5 loopback ──► router/registry ──► direct | pool
                              │
        DNS/53 ──► freedom (bind به interface فیزیکی) ──► resolver مستقیم
        UDP غیر 53 ──► blackhole
```

**سرویس جدا، نه TUN در engine.** TUN در یک process جدا (`routelatch
__tun-service`) و با همان هستهٔ pin‌شده اجرا می‌شود. این جداسازی عمدی است:

- `core.Instance` بعد از `Stop` وضعیت process-global دارد و restart درون‌process
  ممنوع است (R-4). TUN enabled/disabled باید بدون restart engine کار کند؛ سرویس
  جدا یک core تازه در هر enable می‌سازد و با disable خارج می‌شود.
- TUN به `CAP_NET_ADMIN` نیاز دارد. این capability فقط به سرویس کوچک TUN داده
  می‌شود؛ engine، tray و management با root اجرا نمی‌شوند.
- TUN می‌تواند مستقل از engine زنده بماند تا در قطع engine اتصال‌ها fail-closed
  بمانند.

**نصب یک‌باره.** `sudo routelatch tun setup` یک systemd **system** unit/socket
می‌سازد (نه در بسته؛ D26 برای بسته دست‌نخورده می‌ماند). unit با
`CapabilityBoundingSet=CAP_NET_ADMIN` و `AmbientCapabilities=CAP_NET_ADMIN`،
`NoNewPrivileges=yes` و hardening اجرا می‌شود؛ کنترل socket فقط برای همان uid
(peer credential) است و ورودی‌اش فقط listener لوپ‌بک و دستورهای ثابت است. هیچ
route/interface/آدرس دلخواهی از کاربر پذیرفته نمی‌شود. `tun uninstall` unit و
state موقت را پاک می‌کند. جزئیات قراردادی در `contracts.md §۱۰`.

**حلقه و ترافیک خود RouteLatch.** TUN همهٔ ترافیک سیستم را می‌گیرد، پس dialهای
خود engine (pool، direct، probe، refresh) و sidecar نیز باید از TUN بیرون بمانند:

- engine همهٔ dialهای غیرلوپ‌بک را در حالت TUN به interface فیزیکی پیش‌فرض bind
  می‌کند (`SO_BINDTODEVICE`؛ از Linux 5.7 برای کاربر غیرroot مجاز است). این شامل
  مسیر direct، probe، fetch اشتراک و resolver سفارشی است.
- dialهای pool از داخل core با یک controller ثبت‌شده در start (که فقط وقتی TUN
  active است bind می‌کند) یا معادل منظم آن bypass می‌شوند؛ controller یک‌بار در
  هر process ثبت می‌شود و هیچ state سراسری را دوباره‌نویسی نمی‌کند.
- sidecar sing-box با `route.default_interface`/`auto_detect_interface` به همان
  interface bind می‌شود؛ این فیلد از کانفیگ تولیدی ما می‌آید، نه از لینک import.
- خود سرویس TUN فقط به loopback SOCKS و برای DNS به interface فیزیکی dial
  می‌کند؛ بنابراین مسیرش وارد TUN نمی‌شود.

**DNS و UDP.** DNS سیستم تغییر نمی‌کند (D15). کوئری plain DNS (TCP/UDP پورت 53)
توسط Xray TUN دریافت و از طریق `freedom` bind‌شده به interface فیزیکی مستقیم
فرستاده می‌شود؛ بنابراین نام‌ها در شبکهٔ محلی/resolver دیده می‌شوند و «proxied»
نیستند. DoH/DoT روی TCP مسیر عادی SOCKS/pool را طی می‌کنند. UDP غیر از DNS به
blackhole می‌رود: QUIC در فاز اول پشتیبانی نمی‌شود (سیاست no-UDP/QUIC) و برنامه‌ها
باید به TCP سقوط کنند. ICMP Echo توسط stack محلی پاسخ داده می‌شود و اثبات
دسترسی واقعی نیست.

**مسیر بعدی DNS/UDP (D30).** DNS-through-pool و پوشش UDP در فاز اول نیستند و
هر کدام یک task تحقیقی جدا (R16-I/R16-J) دارند. DNS یک switch مستقل خواهد
داشت که فقط مسیر DNS را از pool/مستقیم عوض می‌کند، جدا از mode و سایر ترافیک؛
تا verdict آن، DNS مستقیم می‌ماند. UDP تا verdict، بلاک می‌ماند. هیچ‌کدام بدون
قرارداد/acceptance جدید پیاده نمی‌شوند و D29 را برای فاز اول تغییر نمی‌دهند.

**IPv6.** TUN دو-stack است و routeهای `::/1` و `8000::/1` هم به TUN می‌روند تا
نشت IPv6 نباشد. gatewayهای غیرقابل‌مسیریابی (IPv4 benchmarking و ULA) انتخاب
می‌شوند تا با LAN معمول تصادم نکنند. مقصد IPv6-only از همان exit/pool یا direct
پیروی می‌کند؛ اگر exit IPv6 نداشته باشد خطای روشن می‌دهد.

**دامنه و sniffing.** Xray TUN فقط IP می‌بیند. برای TLS (SNI) و HTTP (Host)
sniffing روشن است و دامنهٔ بازیابی‌شده به SOCKS تحویل می‌شود تا قانون
domain/domain_suffix کار کند. **وقتی دامنه بازیابی شود، همان دامنه هویت مقصد
است و قانون IP/CIDR برای آن flow اضافه اعمال نمی‌شود**؛ برای ECH، QUIC یا پروتکل
بدون metadata دامنه در دسترس نیست و فقط قانون IP/CIDR اعمال می‌شود. این
اولویت دامنه باید در status/docs باشد و پنهان نشود. sniffing فقط metadata همان flow را در حافظه می‌خواند و در
log/state نوشته نمی‌شود (D21).

**چرخهٔ عمر و fail-closed.** `tun on` فقط در mode غیر stopped مجاز است. توقف
(`mode stopped`)، Quit و uninstall اول TUN را می‌بندند و بعد listenerها را؛ اگر
بستن TUN شکست بخورد، توقف fail می‌شود تا سیستم در حالت blackhole رها نشود. با
مرگ/ crash خود engine، سرویس TUN و routeها می‌مانند و SOCKS در دسترس نیست، پس
اتصال‌ها refuse/blackhole می‌شوند (fail-closed). restart engine همان TUN را دوباره
پیدا می‌کند. اگر خود سرویس TUN از کار بیفتد یا restart شود، تا بازسازی TUN یک
پنجرهٔ محدود fail-open وجود دارد؛ status/آیکن باید آن را قرمز/هشدار نشان دهد و
engine باید بازسازی را retry کند. یک kill-switch دائمی (firewall blackhole مستقل)
خارج از فاز اول است و تصمیم جدا می‌خواهد.

**ماندگاری و آمادگی.** انتخاب TUN در config ذخیره می‌شود و پس از restart/login
خودکار فعال می‌شود، اگر helper نصب و مجاز باشد؛ در غیر این صورت TUN خاموش
می‌ماند و status/آیکن warning روشن می‌دهد (بی‌صدا خاموش نمی‌شود). `status.ready`
همچنان آمادگی management/listener را می‌سنجد؛ TUN یک فیلد/زیرحالت جدا دارد و
readiness کل app را به آن گره نمی‌زند، چون نبود helper نباید proxy عادی را از کار
بیندازد.

**tray و CLI.** زیرمنوی Coverage در tray یک toggle «System-wide (TUN)» با وضعیت
on/off/starting/error و دلیل امن دارد؛ رنگ آیکن برای TUN active/error/desired-off
متفاوت است. عملیات متنی و نصب helper در CLI می‌مانند (`routelatch tun setup|
uninstall|status|on|off`)؛ tray فقط پس از نصب helper toggle می‌کند. هیچ مقصد،
URL، نام exit یا credentialی در label/notification/log نمی‌رود (D21/D28).

**محدودیت سطح ۳.** stack پیش از اثبات اتصال مقصد به TCP پاسخ می‌دهد؛ بنابراین
شکست pool در TUN به‌صورت بستن/reset همان flow دیده می‌شود، نه refuse فوری.
هیچ fallback مستقیمی رخ نمی‌دهد، ولی تجربهٔ خطا با listenerهای HTTP/SOCKS یکی
نیست و باید مستند شود (R-27). ICMP Echo هم محلی پاسخ داده می‌شود و reachability
واقعی را ثابت نمی‌کند.

**تغییر شبکه.** در فاز اول، تغییر interface فیزیکی پیش‌فرض (Wi-Fi↔Ethernet،
VPN، suspend/resume) خودکار به bypass جدید منتقل نمی‌شود؛ ممکن است تا
`tun off/on` خطا یا مسیر کهنه بماند. R16-A رفتار route monitor را می‌سنجد و
در غیر این صورت watch/restart تصمیم بعدی است؛ status باید خطا را نشان دهد
(R-28).

**چرا این طراحی.** TUN در همان process engine یک controller سراسری و lifecycle
route را وارد می‌کند که با ممنوعیت restart درون‌process و مدل process-level
D26 ناسازگار است. جداکردن TUN به یک سرویس کوچک با capability محدود، این
ریسک‌ها را از engine دور می‌کند و اجازه می‌دهد سیاست/registry/D13 در engine
واحد بمانند. گزینه‌های ردشده: TUN داخل engine (state سراسری/restart)، اجرای
engine با root (سطح حمله)، و sing-box به‌عنوان موتور TUN اصلی (وابستگی GPL و
مدل سیاست دوم).
