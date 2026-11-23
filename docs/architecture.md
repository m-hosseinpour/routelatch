# معماری و امنیت

طرح ۲۰۲۶-۱۱-۱۷. تصمیم‌های محصول در [نیازمندی‌ها](requirements.md) مرجع‌اند؛
قراردادهای اجرایی، اعداد و ماتریس‌ها در [قراردادها](contracts.md) هستند و این سند
فقط مرز اجزا و «چرا» را نگه می‌دارد. نسخه‌های pin و شاهدهای `T1` در
[تحقیق](research.md) و `spike/*/FINDINGS.md` ثبت شده‌اند.

## ۱. مرز مسئولیت‌ها

```text
CLI / منوی huh / GUI آینده
            │ management Unix socket + JSON (0600، SO_PEERCRED)
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
- DNS سیستم، firewall و TUN تغییر نمی‌کنند.

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
> پیاده‌سازی در R13 باز است؛ بسته‌های منتشرشده تا آن زمان همان unit و `run`
> foreground را دارند و README/INSTALL/SERVICE رفتار فعلی را توصیف می‌کنند.

- Linux `amd64`+`arm64`؛ root/TUN لازم نیست. `routelatch run` پیش‌فرض اپ است: از
  ترمینال detach می‌شود، tray (و بعداً GUI) نشان می‌دهد و engine را به‌عنوان child
  `routelatch run --headless` اجرا و supervise می‌کند. `--headless` تنها حالت
  جایگزین است: engine foreground بدون tray/GUI برای ترمینال، SSH، container، تست و
  systemd. فرمان جدا برای `tray`/`gui` وجود ندارد؛ اگر GUI بعداً اضافه شود، همان
  `run` پیش‌فرض GUI+tray می‌شود و `--headless` دست‌نخورده می‌ماند (D26).
- launcher با `setsid`/readiness از ترمینال جدا می‌شود و فقط پس از bind listenerها و
  ثبت tray برمی‌گردد؛ خطای startup با exit code به فراخواننده می‌رسد. engine child
  state lock و management socket قبل را دارد؛ invariant دقیق «یک engine در هر data
  directory» است، نه هر کاربر. همهٔ lock/socket/app files از یک `InstanceDir`
  مشتق از state/data directory (`<StateDir>/run` یا `<data-dir>/run`) می‌آیند، نه
  از `XDG_RUNTIME_DIR`؛ بنابراین desktop و SSH با runtime env متفاوت همان lock و
  socket را می‌بینند. app-mode یک `flock` روی `InstanceDir/app.lock` و یک marker
  تشخیصی/`pid` در همان‌جا می‌نویسد؛ liveness را lock تعیین می‌کند، نه PID؛
  supervisor مالک lock است و engine child آن را به ارث نمی‌برد. Quit یا مرگ supervisor باید child را متوقف/reap کند (parent-death یا
  watchdog) تا listener/management یتیم نماند. restart فقط با جایگزینی process انجام
  می‌شود (tray child یا systemd)؛ restart درون‌process ممنوع می‌ماند (R-4). بستن
  ترمینال پس از readiness پروسهٔ detached را متوقف نمی‌کند.
- tray با StatusNotifierItem/D-Bus و کتابخانهٔ pure Go هدف‌گذاری می‌شود تا
  `CGO_ENABLED=0` حفظ شود. GNOME بدون extension و SSH/بدون display tray ندارند؛
  `run` پیش‌فرض در این محیط‌ها با پیام روشن fail می‌شود و `--headless` پیشنهاد
  می‌دهد؛ fallback بی‌صدا وجود ندارد. KDE مسیر آزمون اولیه است؛ سایر DEها تا آزمون
  واقعی ادعا نمی‌شوند.
- شروع در login با XDG autostart است، نه unit: `routelatch autostart enable` یک فایل
  `$XDG_CONFIG_HOME/autostart/routelatch.desktop` (fallback `~/.config/autostart`)
  با `Exec=<absolute> run` می‌سازد. نصب بسته autostart نمی‌سازد؛ linger/start-before-login
  بخشی از مدل دسکتاپ نیست.
- برای VPS/server یک سرویس opt-in وجود دارد: `routelatch service install` یک systemd
  **user unit** در `$XDG_CONFIG_HOME/systemd/user` (fallback `~/.config/systemd/user`)
  با `ExecStart=<absolute> run --headless` می‌سازد، enable/start می‌کند و اگر linger
  خاموش باشد فقط `sudo loginctl enable-linger <user>` را چاپ می‌کند؛ خودش sudo
  نمی‌زند. unit با یک marker مالکیت مشخص می‌شود؛ uninstall فقط unit مالک را حذف
  می‌کند. `autostart` و `service` برای یک کاربر هم‌زمان فعال نمی‌شوند و install هر
  کدام دیگری را detect و refuse می‌کند. unit نصب‌شده بخشی از بسته نیست و بستهٔ فعلی
  R12 unit دارد؛ حذف آن در R13 انجام می‌شود. جزئیات در `docs/contracts.md §۷.۵` است.
- آرتیفکتهای `riscv64` و `ppc64le` فقط cross-build و runtime-untestedاند؛ پشتیبانی runtime نسخهٔ v1 همان amd64/arm64 است. بستهٔ `.deb` محلی محتوای tarball را به `/usr` نگاشت میکند (`bin`→`/usr/bin`، `share/doc`→`/usr/share/doc`، `share/routelatch`→`/usr/share/routelatch`)، در R12 unit را دارد و از R13 طبق D26 هیچ unit/postinst یا فعالسازی سرویسی ندارد؛ sidecar فقط برای amd64/arm64 قرار میگیرد و `.deb` پاورپیسی با نام Debian یعنی `ppc64el` ساخته میشود. اسپک RPM برای هر چهار معماری (`amd64`→`x86_64`، `arm64`→`aarch64`، `riscv64` و `ppc64le` بدون تغییر) از همان BUILD-INFO ساخته و در SHA256SUMS ثبت میشود؛ VERSION آن دقیقاً برابر `.deb` است. LICENSE/NOTICE زیر `/usr/share/licenses/routelatch` و بقیهٔ اسناد زیر `/usr/share/doc/routelatch` نصب میشوند، بدون scriptlet یا فعالسازی سرویس. rpmbuild روی host نصب نیست؛ باینری RPM در container یک‌بارمصرف ساخته، بازرسی و payload آن اجرا شده است، CI نیز باینری‌ها را می‌سازد/بازرسی می‌کند، و انتشار آن مجوز جدا می‌خواهد. نصب واقعی تحت RPM و اجرای سرویس opt-in روی معماری‌های غیر amd64 هنوز آزمون‌نشده‌اند.
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

TUN و UDP semantics جدا دارند (capture، DNS، privilege، teardown) و registry اتصال
v1 را به‌طور خودکار به دست نمی‌آورند؛ D13 روی TUN دروازهٔ فنی جدید است. Windows به
named-pipe/ACL و SCM نیاز دارد. Android `VpnService` بومی و یک Go runtime در artifact
می‌خواهد. GUI بعداً همان `run` پیش‌فرض را به GUI+tray تبدیل می‌کند و `--headless`
دست‌نخورده می‌ماند؛ فرمان `gui` جدا وجود ندارد. انتخاب framework سرمایه‌گذاری جدید و
خارج از v1 است (D24/D26).
