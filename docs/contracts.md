# قراردادهای فنی و امنیتی RouteLatch

سند تصمیم‌های `T2` — ۲۰۲۶-۱۱-۱۷. مرجع پیاده‌سازی `T3`–`T6` و بازبینی آن‌ها.

این سند **قرارداد** است، نه گزارش اجرا: هر عدد یا رفتار «اندازه‌گیری‌شده» به شاهد `T1`
در `spike/*/FINDINGS.md` ارجاع دارد؛ هر چیز دیگر تصمیم مهندسی است که با شواهد جدید
بازبینی می‌شود، نه با حدس. تصمیم‌های محصول در [نیازمندی‌ها](requirements.md) مرجع‌اند و
این سند هیچ `D`-item را ضعیف نمی‌کند؛ جایی که تضمین از آنِ ما نیست، صریح نام‌گذاری شده
است. `internal/contract` نسخهٔ Go همین سند است: نام تایپ‌ها، فیلدها، kindهای خطا و
اینترفیس‌ها باید با بخش‌های این سند یکی بمانند.

## ۰. دامنه، مرز و نسخه‌ها

| مورد | تصمیم |
| --- | --- |
| پلتفرم v1 | Linux `amd64` و `arm64`؛ Go `1.27.4` با `GOTOOLCHAIN=local` |
| هستهٔ موتور | `github.com/xtls/xray-core v1.260327.1-0.20261109052709-676eaf66f8f0` (کامیت tag `v26.11.9`) |
| SOCKS5 | `github.com/things-go/go-socks5 v0.1.4` با resolver بدون lookup و `WithDialAndRequest` |
| HTTP | `net/http` استاندارد؛ `goproxy v1.9.4` رد شده و در محصول نیست |
| منو | `github.com/charmbracelet/huh v1.0.0` |
| `golang.org/x/net` | `v0.62.0` به‌صورت direct (برای `publicsuffix`؛ `idna` و `proxy` هم از همین ماژول) |
| وابستگی جدید | ممنوع بدون تغییر قرارداد |

**مرز بسته‌بندی R12:** آرتیفکت‌های `riscv64` و `ppc64le` فقط cross-build و runtime-untestedاند و ماتریس پشتیبانی runtime نسخهٔ v1 (`amd64`/`arm64`) را تغییر نمیدهند. بستهٔ `.deb` محلی محتوای همان tarball را زیر `/usr` نصب می‌کند؛ بسته‌های منتشرشده تا `v0.1.3` unit دارند و از `v1.0.0` طبق D26 هیچ user unit/postinst ندارند. سرویس فقط با `routelatch service install` (user unit، D26) به‌صورت opt-in ساخته می‌شود و فعال‌سازی سرویس/تنظیم دسکتاپ در نصب نیست. برای RPM یک اسپک در هر معماری (`x86_64`، `aarch64`، `riscv64`، `ppc64le`) ساخته و در SHA256SUMS ثبت میشود؛ Version آن دقیقاً برابر Version `.deb` است (نرمالسازی واحد: `v0.1.0-r12.1` → `0.1.0.r12.1`). `%files` LICENSE/NOTICE را در `/usr/share/licenses/routelatch` و بقیهٔ اسناد را در `/usr/share/doc/routelatch` قرار میدهد و هیچ scriptlet یا %post ندارد. باینری RPM روی host ساخته نمی‌شود (rpmbuild نصب نیست)؛ ساخت/بازرسی محلی فقط در container یک‌بارمصرف انجام شده است و CI نیز آن را با rpmbuild می‌سازد و با rpm بازرسی می‌کند.

ماژول‌های بالا در `go.mod` پین‌اند؛ `internal/contract/pins.go` با build tag `tools`
فقط برای این وجود دارد که `go mod tidy` قبل از ورود importکننده‌ها آن‌ها را حذف نکند.

**مرز اسناد:** این سند مالک semantics مسیر، خطا، state، pool، probe، CLI و ماتریس
پلتفرم است. `architecture.md` نمای کلی و مرز اجزا را نگه می‌دارد؛ `research.md` snapshot
تحقیق است و به‌روزرسانی نمی‌شود. زمان‌بندی و وضعیت کار فقط در `implementation-plan.md`
است.

**اینترفیس‌های فریزشده** (همه در `internal/contract`؛ هیچ packageی حق ویرایش آن را
ندارد، فقط پیاده‌سازی):

| اینترفیس | پیاده‌ساز | بخش |
| --- | --- | --- |
| `Router` (تصمیم مسیر) | T3 `internal/routing` | §۲.۵ |
| `Dialer` (مسیر مستقیم) | T3 | §۲.۵، §۱.۴ |
| `PoolDialer` (dial با کلاس اثبات) | T4 `internal/pool` | §۲.۱، §۳ |
| `Registry` + `ConnHandle` | T3 `internal/connections` | §۲.۶ |
| `Store` + `State`/`Config`/`Snapshot` | T3/T4 | §۴ |
| `Prober` + `ProbeRequest`/`ProbeResult` | T4 `internal/probes` | §۶ |
| `Fetcher`، `Importer`، `Source`، `Record` | T4 `internal/imports` | §۵ |
| `Failure` + `ErrorKind` + `ExitCodeFor` | مشترک؛ همه | §۳، §۷.۲ |

**قراردادهای باز (عمداً در `internal/contract` نیستند):** API داخلی `internal/engine`
(tagسازی، ساخت کانفیگ، ثبت distro)، پیاده‌سازی `internal/proxy` روی `net/http` و
`go-socks5`، و شکل فریم‌های منوی تعاملی. این‌ها با همین سند سازگارند ولی مالکشان
`T3`/`T5` است.

## ۱. مدل مقصد

### ۱.۱ انواع

```go
type Destination struct {
    Kind   DestKind    // DestDomain | DestIP | DestCIDR
    Host   string      // فقط DestDomain؛ A-label lowercase بدون نقطهٔ انتهایی
    Addr   netip.Addr  // DestIP؛ برای CIDR آدرس شبکه
    Prefix netip.Prefix // فقط DestCIDR
    Port   uint16      // ۰ = ذکر نشده
}
```

قانون‌ها (`Rule`) یکی از `RuleDomain`، `RuleDomainSuffix`، `RuleIP`، `RuleCIDR` هستند.
قانون در v1 **هیچ‌گاه پورت ندارد**: یک قانون دامنه همهٔ پورت‌ها را پوشش می‌دهد و
`Rule` هیچ فیلد پورتی ندارد. تفکیک ورودی عمدی است:

- operand مستقیمِ دارای پورت (`example.com:443`، `[2001:db8::1]:443`) رد می‌شود، چون
  کاربر با نوشتن پورت انتظار یک قانون باریک‌تر دارد؛ پذیرش بی‌صدا دامنهٔ قانون را
  گسترده می‌کند.
- ورودی URL یک «مقصد» است، نه یک قانون با دامنهٔ پورت‌دار: پورت آن سرویس مقصد را نام
  می‌برد، پس در یکسان‌سازی قانون **حذف** می‌شود و notice فرمان باید همین را بگوید (§۱.۳).
  برای `import` پورت بخشی از رکورد است.

در هر دو حالت خروجی `Rule` بدون پورت است و تطبیق §۱.۵ هر پورتی را پوشش می‌دهد.

### ۱.۲ خط لولهٔ یکسان‌سازی

تابع واحد: `routing.NormalizeDestination(raw string) (contract.Destination, error)` و
`routing.NormalizeRule(raw string) (contract.Rule, error)`. هیچ مسیر دیگری نباید
hostname را دستکاری کند.

| گام | قاعده |
| --- | --- |
| N1 | `strings.TrimSpace`؛ خالی یا > ۲۰۴۸ بایت → `invalid_destination` |
| N2 | اگر `://` دارد → مسیر URL (§۱.۳) |
| N3 | اگر با `[` شروع شود: `]` را پیدا کن؛ داخل با `normalizeAddr` بررسی می‌شود (باید IPv6 باشد؛ IPv4-mapped پس از unmap به IPv4 نرمال می‌شود)، باقی خالی یا `:port`؛ براکت روی نام → invalid |
| N4 | اگر `/` دارد: `netip.ParsePrefix` موفق → `DestCIDR` (host bits با `.Masked()`؛ IPv4-mapped با unmap و کاهش ۹۶ بیت)؛ ناموفق → invalid |
| N5 | اگر `normalizeAddr` کل ورودی را پذیرفت → `DestIP` |
| N6 | اگر دقیقاً یک `:` دارد: اگر پسوند عددی ۱..۶۵۵۳۵ است، host را جدا کن و پورت را برای نتیجه نگه دار؛ اگر host با `normalizeAddr` معتبر بود → `DestIP` + پورت، وگرنه با همان پورت به N8 برو. اگر پسوند غیرعددی است: اگر بخش قبل از `:` یک برچسب بدون نقطه و با الگوی `^[a-z][a-z0-9+.-]*$` است → `unsupported`، وگرنه invalid (`example.com:https` → invalid). IPv6 بدون براکت در N5 آدرس است، نه host:port |
| N7 | اگر بیش از یک `:` دارد و با براکت نیست: اگر بخش قبل از اولین `:` یک برچسب بدون نقطه و scheme-like است → `unsupported` (مثل `vless:uuid@host:443`)، وگرنه invalid |
| N8 | whitespace/کنترل داخل ورودی → invalid (فقط trim دو سر در N1)؛ `/ @ ? # *` → invalid؛ حداکثر یک نقطهٔ انتهایی حذف؛ `..` یا برچسب خالی → invalid |
| N9 | lowercase؛ `idna.Lookup.ToASCII`؛ خطای IDNA → invalid؛ هر برچسب ۱–۶۳، کل ≤ ۲۵۳ اکتت، فقط `[a-z0-9-]`، بدون `-` در دو سر |
| N10 | `localhost`/`*.localhost` → `ClassLocal` (پیش از PSL)؛ اگر آخرین برچسب تماماً رقم است **و میزبان IP نبود** → invalid (شکل عددی قدیمی) |
| N11 | اگر ≥۲ برچسب دارد و `publicsuffix.PublicSuffix(host)` برابر host است → invalid (public/private suffix مثل `co.uk`، `github.io`) |
| N12 | نام تک‌برچسبی: اگر TLD واقعی باشد (`PublicSuffix` با `icann=true` و برابر host، مثل `com`) → invalid؛ وگرنه (`intranet`) → `ClassLAN` |
| N13 | پورت صریح `0` یا خارج از `1..65535` → invalid |

`normalizeAddr(raw)` یک تابع است که N3/N5/N6 همه از آن استفاده می‌کنند: `netip.ParseAddr`،
سپس `.Unmap()`، سپس رد zone و آدرس unspecified (`0.0.0.0`، `::`). هیچ مسیری نباید
`ParseAddr` را جداگانه صدا بزند، وگرنه `0.0.0.0:80` از قاعدهٔ N5 فرار می‌کند.

`NormalizeRule` پس از N6 اگر پورت صریح دید، قانون را رد می‌کند («قانون بدون پورت
است»). همهٔ ارقام IPv4 باید dotted-quad استاندارد باشند: شکل‌های قدیمی (`127.1`،
`2130706433`، `0x7f.1`، `0177.0.0.1`) با قاعدهٔ N10 **invalid** هستند و هرگز به
loopback یا آدرس دیگری تبدیل نمی‌شوند، تا ورودی نتواند از قانون فرار کند. CIDR قبل از
قاعدهٔ کاراکترهای ممنوع بررسی می‌شود، وگرنه `/` آن را رد می‌کرد.

**انتخاب صریح نوع قانون.** `NormalizeRule` فقط استنتاج می‌کند: IP → `RuleIP`، CIDR →
`RuleCIDR`، hostname → `RuleDomain` دقیق؛ هیچ‌گاه suffix استنتاج نمی‌شود. مسیر
پشتیبانی‌شدهٔ ساخت قانون با نوع صریح (از جمله `RuleDomainSuffix`) تابع
`routing.NormalizeRuleAs(kind, raw)` است؛ همان خط لولهٔ یکسان‌سازی را اجرا می‌کند و
ناسازگاری kind با operand یا وجود پورت را رد می‌کند. `*.example.com` همچنان invalid
است و کاربر باید suffix را صریح انتخاب کند.

### ۱.۳ URL → hostname

- فقط `http://` و `https://` (بدون حساسیت به حروف scheme) پذیرفته می‌شوند (`url.Parse` +
  `u.Hostname()`).
- مسیر، query، fragment و userinfo دور ریخته می‌شوند و **ذخیره یا لاگ نمی‌شوند**.
- پورت URL برای قانون **حذف** می‌شود (نه رد)، چون URL یک مقصد است و پورت آن سرویس را
  نام می‌برد، نه دامنهٔ قانون؛ notice فرمان باید این را بگوید. برای `import` پورت بخشی از
  رکورد است. operand مستقیم `host:port` همچنان به‌عنوان قانون رد می‌شود (§۱.۱).
- `vless://`, `vmess://`, `trojan://`, `ss://`, `socks://` در `rules add` →
  `unsupported` با پیام «از import استفاده کن».
- `ftp://`, `file://` و scheme ناشناس → `unsupported` (هیچ fetch یا خواندن فایل).

### ۱.۴ کلاس مقصد و ترتیب اولویت

`ClassSelf > ClassLocal > ClassLAN > ClassApproved > ClassOther`. کلاس **قبل** از
تطبیق قانون محاسبه می‌شود و قانون کاربر هیچ‌گاه کلاس محلی را بازنویسی نمی‌کند.

| کلاس | تعریف | نتیجه در همهٔ حالت‌های دارای listener |
| --- | --- | --- |
| Self | IP loopback یا نام `.localhost` با پورت یکی از listenerهای خودمان | refuse (`policy_denied`)؛ جلوگیری از loop |
| Local | `127/8`، `::1`، `169.254/16`، `fe80::/10`، multicast، broadcast، `localhost`/`*.localhost` | direct |
| LAN | `10/8`، `172.16/12`، `192.168/16`، `100.64/10`، ULA `fc00::/7`، سایر unicast غیرعمومی، نام تک‌برچسبی | direct |
| Approved | مقصد عمومی که قانونی را match می‌کند | طبق ماتریس §۲.۵ |
| Other | سایر مقصدهای عمومی | طبق ماتریس §۲.۵ |

`0.0.0.0` و `::` به‌عنوان مقصد **invalid** هستند، نه Local: در بسیاری از سیستم‌ها
dial به `0.0.0.0` عملاً loopback است و نباید راه دسترسی به سرویس محلی شود.

**Self پورت listenerهای پیکربندی‌شده را لازم دارد.** router فقط از روی loopback بودن
تصمیم نمی‌گیرد: `routing.Config.SelfPorts` باید پورت‌های listener ترافیک ما (HTTP و
SOCKS) را داشته باشد. loopback روی هر پورت دیگری `ClassLocal` است و مستقیم می‌ماند؛
پس نبودِ یک پورت در این فهرست یعنی «مقصد محلی دیگر»، نه «Self». management در v1 یک
Unix socket است و مقصد TCP نیست؛ اگر در آینده سطحی از management روی TCP بیاید، پورت
آن هم باید در `SelfPorts` باشد. تصمیم Self همیشه `Route=refuse` با
`Reason=self_loop` است و frontend آن را `policy_denied` (HTTP `403` / SOCKS `0x04`)
می‌کند. پیام redacted خطا شامل `self_loop` است و `ConnInfo.Reason` همین را نگه
می‌دارد؛ هر سطحی که جزئیات اتصال یا خطا را نشان می‌دهد (CLI/لاگ/status) باید آن را
حفظ کند. پاسخ روی سیم عمداً فقط `policy_denied` عمومی است و جزئیات مسیر را لو نمی‌دهد.

**حلقه از طریق DNS مستقیم:** مسیر مستقیم hostname را با resolver سیستم حل می‌کند، پس
`NormalizeDestination` به‌تنهایی برای Self کافی نیست. `dialDirect` پس از resolve،
آدرس‌های به‌دست‌آمده را بررسی می‌کند: اگر یکی از آن‌ها loopback باشد و پورت برابر پورت
یکی از listenerهای ما، اتصال با `policy_denied` رد می‌شود (بدون dial). resolve به
loopback/LAN در بقیهٔ موارد مجاز است، چون همان رفتار خود برنامه است؛ اما هرگز نباید
دوباره به listener خودمان برسد. هیچ reverse DNS برای این تصمیم انجام نمی‌شود.

### ۱.۵ تطبیق قانون

- `RuleDomain`: `dst.Host == rule.Host`.
- `RuleDomainSuffix`: `dst.Host == rule.Host` یا `strings.HasSuffix(dst.Host, "."+rule.Host)`؛
  مرز برچسب اجباری است (`badexample.com` با قانون `example.com` match نمی‌شود).
- `RuleIP`: `dst.Kind == DestIP && dst.Addr == rule.Addr` (پس از unmap).
- `RuleCIDR`: `dst.Kind == DestIP && rule.Prefix.Contains(dst.Addr)`.
- hostname هرگز با قانون IP/CIDR مطابقت داده نمی‌شود، چون RouteLatch آن را resolve
  نمی‌کند. reverse DNS و IP مشترک CDN اثبات دامنه نیستند.

### ۱.۶ جدول ورودی‌های مرزی

| ورودی | نتیجه | کلاس | قانون پذیرفته؟ |
| --- | --- | --- | --- |
| `API.Example.COM` | `api.example.com` | Other | بله |
| `api.example.com.` | `api.example.com` | Other | بله |
| `api.example.com..` | invalid | — | نه |
| `.example.com` / `api..example.com` | invalid | — | نه |
| `*.example.com` | invalid (suffix صریح انتخاب کن) | — | نه |
| `com` / `co.uk` | invalid (public suffix) | — | نه |
| `example` | LAN (نام تک‌برچسبی) | LAN | نه |
| `localhost` / `api.localhost` | Local | Local | نه |
| `[::1]` / `[::1]:443` | `::1` / `::1` با پورت ۴۴۳ | Local | نه |
| `[2001:db8::1]:443` | `2001:db8::1` پورت ۴۴۳ | Other | IP بله |
| `0.0.0.0` / `::` | invalid | — | نه |
| `0.0.0.0:80` / `[::]:80` | invalid (unspecified، حتی با پورت) | — | نه |
| `127.0.0.1` | Local | Local | نه (بی‌اثر و گمراه‌کننده) |
| `127.0.0.1:80` / `192.168.1.10:443` | `DestIP` با پورت | Local / LAN | — |
| `203.0.113.8:443` | `203.0.113.8` با پورت ۴۴۳ | Other | IP بله |
| `127.1`، `2130706433`، `0x7f.1`، `0177.0.0.1` | invalid | — | نه |
| `169.254.1.1` / `fe80::1` | Local (link-local) | Local | نه |
| `fe80::1%eth0` | invalid (zone) | — | نه |
| `fd00::1` / `100.64.0.1` | LAN | LAN | نه |
| `::ffff:192.168.1.1` | `192.168.1.1` | LAN | نه |
| `::ffff:8.8.8.8` | `8.8.8.8` | Other | بله |
| `203.0.113.0/24` | CIDR | — | بله (IP عمومی) |
| `203.0.113.8/33` | invalid | — | نه |
| `2001:db8::/32` | CIDR | — | بله |
| `example.com:443` (به‌عنوان قانون) | قانون رد می‌شود («بدون پورت»)؛ به‌عنوان مقصد معتبر است | — | نه |
| `api.example.com:443` (CONNECT) | `api.example.com` با پورت ۴۴۳ | Other | — |
| `example.com:https` | invalid (پورت غیرعددی) | — | نه |
| `2001:db8::1:443` | IPv6 معتبر `2001:db8::1:443`، بدون پورت؛ برای پورت باید `[...]` بدهید | Other | IP بله |
| `https://user:pass@Example.com:8443/p?q=1#f` | `example.com` (اعتبارنامه دور ریخته) | Other | بله (دامنه) |
| `http://[2001:db8::1]/` | `2001:db8::1` | Other | بله |
| `ftp://example.com` | unsupported | — | نه |
| `vless://uuid@host:443` | unsupported در rules | — | نه |
| `example.com/path` / `example.com?x` | invalid | — | نه |
| فاصله/خالی | invalid | — | نه |
| نام > ۲۵۳ اکتت یا برچسب > ۶۳ | invalid | — | نه |

### ۱.۷ محدودیت «مصرف‌کننده خودش resolve کرد»

اگر کلاینت به‌جای hostname یک IP بفرستد، تصمیم روی همان IP گرفته می‌شود؛ اگر آن IP
در فهرست نباشد، مستقیم می‌رود حتی اگر دامنهٔ اصلی تأییدشده باشد (D10/A02). sniffing
پایهٔ تضمین نیست و با ECH کار نمی‌کند (`E12`). SNI و Host هدر فقط برای عبور به خروجی
حفظ می‌شوند، نه برای مسیر. resolver خروجی و policy آن خارج از کنترل ماست؛ ادعای ما
دربارهٔ «چه hostname‌ای فرستادیم» است، نه «چه IP‌ای در خروجی حل شد».

**DNS سرور خروجی جدا است:** برای بازکردن تونل، hostname خود سرور exit ناچار با resolver
محلی حل می‌شود. این با DNS مقصد پروکسی‌شده یکی نیست؛ ادعای «هیچ DNS محلی» فقط برای
مقصد صادق است و همین محدودیت در A08 گزارش می‌شود.

## ۲. کلاس اثبات handshake و ماتریس مسیر

### ۲.۱ دو کلاس اثبات

| کلاس | معنا | منابع v1 | چه چیزی ثابت می‌شود |
| --- | --- | --- | --- |
| `ProofConfirmed` | reply خود خروجی موفقیت را گزارش کرد (پس از connect، در خروجی سازگار) | SOCKS5/HTTP CONNECT که reply را خودمان parse می‌کنیم (HTTP پس از گیت R-2) | «خروجی موفقیت را گزارش کرد» — assertion خروجی، نه تأیید مستقل |
| `ProofAccepted` | خروجی درخواست را پذیرفت و stream قابل‌استفاده داد؛ پروتکل ack مقصد ندارد | VLESS/Trojan در هستهٔ pin‌شده | فقط «dispatch پذیرفته شد»؛ نه دسترسی مقصد |

**مسیر پیاده‌سازی هر کلاس:** خروجی‌های `socks5`/`http` را adapter کوچک خارج از هسته
dial می‌کند، چون هسته هیچ رویداد مثبتی نمی‌دهد؛ همین adapter کلاس `ProofConfirmed` را
می‌سازد. SOCKS5 با client عمومی موجود (`golang.org/x/net/proxy`، همان ماژول گراف؛
hostname را با ATYP=domain می‌فرستد و فقط بعد از reply موفق برمی‌گردد) و HTTP CONNECT
با handshake کوچک خودمان (کتابخانهٔ آماده‌ای در گراف نیست). VLESS/Trojan از هستهٔ
pin‌شده می‌روند و همیشه `ProofAccepted` می‌گیرند. برای adapter خودمان `Tag` موتور خالی
است و «انتخاب صریح» با `ExitID` بیان می‌شود؛ قاعدهٔ tag صریح و ممنوعیت balancer برای
مسیرهای core-backed است.

شاهد `T1`: `core.Dial` در ۲۲٫۷–۳۳٫۸µs با `err=nil` برمی‌گردد، مقصد ~۵۰۲ms بعد (VLESS)
و ~۱۰۳ms بعد (Trojan) لمس می‌شود؛ با خروجی کاملاً مرده هم `nil` و صفر تماس با مقصد
دیده شد. پس **برگشت Dial هیچ‌گاه `ProofConfirmed` نیست**. برای VLESS/Trojan حتی
«خروجی پذیرفت» قابل مشاهده نیست؛ `ProofAccepted` صادقانه یعنی «هسته درخواست را پذیرفت
و تا این لحظه خطای transport نداده». تصمیم کاربر این کلاس را «خروجی پذیرفت» نامیده
است؛ چون شاهد T1 نشان می‌دهد این پذیرش هم قابل مشاهده نیست، در کد، status و UI فقط
«accepted (unconfirmed)» گفته می‌شود و هرگز به «خروجی وصل شد» یا «مقصد در دسترس است»
بسط داده نمی‌شود.

**هشدار conformance (R-11):** هیچ کلاینتی نمی‌تواند تشخیص دهد سرور SOCKS5/HTTP پیش از
connect reply می‌فرستد یا بعد از آن؛ Xray خودش سرور SOCKS را پیش از dispatch پاسخ
می‌دهد (T1). پس `ProofConfirmed` یعنی «خروجی گفت موفق شد»، نه «ما مستقل تأیید کردیم».
status/log باید همین جمله را بگویند؛ اگر کاربر به خروجی اعتماد ندارد، تنها گزینهٔ امن
حذف رکورد است، نه تغییر برچسب. اگر یک رکورد `socks5` به inbound خود Xray اشاره کند،
adapter همان `0x00` را می‌بیند و `ProofConfirmed` برمی‌گرداند؛ R-11 همین را پوشش
می‌دهد و ادعای «مقصد در دسترس است» ممنوع می‌ماند.

**`PoolDialDeadline` دقیقاً چه چیزی را می‌بندد.** این مقدار bound فراخوانی dial مسدود
است و فقط برای adapter SOCKS5 خودمان یک bound واقعی handshake است: greeting، احراز
هویت و CONNECT باید پیش از برگشت تمام شوند. مسیر core هیچ فاز dial قابل مشاهده‌ای
ندارد؛ `core.Dial` پیش از تماس با خروجی برمی‌گردد، پس این deadline نمی‌تواند یک dial
گیرکردهٔ core را در فاز dial تشخیص دهد. اگر session core بعد از برگشت گیر کند، خطا در
مرحلهٔ تونل به‌صورت `timeout` یا `destination_unconfirmed` دیده می‌شود، نه به‌عنوان
expired dial deadline. `DirectDialDeadline` فقط dial مستقیم را می‌بندد.

### ۲.۲ زمان reply به کلاینت

| وضعیت | HTTP CONNECT | SOCKS5 | توضیح |
| --- | --- | --- | --- |
| `ProofConfirmed` | بعد از reply خروجی `200 Connection established` | بعد از reply خروجی `0x00` | فقط «خروجی موفقیت را گزارش کرد»؛ نه «مقصد در دسترس است» |
| `ProofAccepted` | بعد از برگشت پذیرش dispatch `200` | بعد از پذیرش `0x00` | باید در status/log/diagnostic/docs «accepted، مقصد تأیید نشده» نامیده شود؛ هرگز «مقصد در دسترس است» |
| خطای پیش از reply | `502`/`403`/`504` طبق §۳ | `0x04`/`0x07` طبق §۳ | هیچ `2xx`/`0x00` قبل از خطا نوشته نمی‌شود |
| خطای پس از reply | فقط بستن تونل؛ هیچ متن خطایی داخل تونل | فقط بستن | `T1 C13` |

**CONNECT: تعهد در اولین بایت مقصد است، نه در `200`.** تونل HTTP CONNECT یک پنجرهٔ
بازیابی دارد. `200` پس از dial موفق با proof نوشته می‌شود، چون کلاینت conforming تا دیدن
`2xx` هیچ بایتی نمی‌فرستد (RFC 9110 §9.3.6)؛ منتظرماندن برای بایت مقصد پیش از `200` هر
TLS handshake را deadlock می‌کند و هر اتصال را یک انتظار کامل کند می‌کند. تضمین به جای
زمان `200`، به «تعهد بایت» بسته شده است: تا اولین بایت مقصد، هیچ بایت کلاینت تعهدشده
نیست. frontend بایت‌های کلاینت را تا `ConnectReplayBufferBytes` (۶۴KiB) بافر می‌کند و
هم‌زمان به exit جاری می‌فرستد. اگر آن exit پیش از هر بایت مقصد تونل را ببندد، همان
بایت‌ها به exit دیگری از انتخاب خودکار replay می‌شوند؛ حداکثر `ConnectFailoverExits`
(۳ exit، یعنی ۲ retry) برای هر CONNECT. فقط پاسخ exit موفق به کلاینت می‌رسد، هیچ درخواستی
از دید کلاینت دوبار دیده نمی‌شود، و fallback مستقیم هرگز رخ نمی‌دهد (D11). با رسیدن
اولین بایت مقصد، یا پر شدن buffer (که replay را ناامن می‌کند)، exit جاری commit می‌شود و
teardown معمولی §۳.۳ اعمال می‌شود. این پنجره فقط مسیر pool و فقط HTTP CONNECT است؛
forwarder سادهٔ HTTP هیچ بدنه‌ای replay نمی‌کند و SOCKS5 همچنان یک تلاش دارد. replay
«حداکثر یک‌بار از دید کلاینت» است: exitی که پیش از مرگ بایت‌ها را دریافت کرده ممکن است
آن‌ها را به مقصد رسانده باشد، پس مقصد می‌تواند همان درخواست را دو بار ببیند؛ هیچ پاسخ
exit شکست‌خورده به کلاینت نمی‌رسد.

**pin دستی و failover.** انتخاب دستی (`pool select`) پنجرهٔ failover را خاموش می‌کند: خطای
pin همان‌جا سطحی می‌شود (بستن تونل پس از `200`) و هرگز به exit دیگری منتقل نمی‌شود؛
همان نمونهٔ شکست pre-byte برای همان exit ثبت و cooling می‌شود، ولی انتخاب pin با cooling
عوض نمی‌شود. دلیل محافظه‌کارانه است: pin تصمیم صریح کاربر است و تبدیل بی‌صدای آن به exit
دیگر همان «سوییچ پنهان»ی است که §۲.۵ ممنوع کرده است.

**ثبت exit در registry.** اتصال از لحظهٔ پذیرش با exit جاری (اولین تلاش) در registry
شناخته می‌شود و با هر retry به exit همان تلاش به‌روز می‌شود؛ `SetExit` آخرین مقدار
غیرخالی را نگه می‌دارد. proof فقط در commit (اولین بایت مقصد) ثبت می‌شود. بنابراین
sweep حذف/refresh یک exit می‌تواند تونل زندهٔ همان exit را پیدا و ببندد، حتی پیش از
اولین بایت؛ اگر policy در میانهٔ پنجره عوض شود، dial بعدی با fence قدیمی `canceled`
می‌شود و اتصال بسته می‌شود (§۲.۶).

**این پنجره time-bounded نیست.** هیچ read deadline روی تونل کاربر گذاشته نمی‌شود؛
پنجره با اولین بایت مقصد، پر شدن buffer، پایان بودجهٔ ۳ exit، بستن کلاینت/exit یا
تغییر policy تمام می‌شود. مقصدی که عمداً ساکت می‌ماند، تونل را نامتعهد نگه می‌دارد
(مثل تونل idle) و بایت‌هایی که دقایقی بعد از کلاینت می‌رسند همچنان قابل replay‌اند؛
این محدودیت در R-15 ثبت است.

**چرا «گیت first-byte» قبلی رد شده بود و این یکی نیست.** گیت قبلی منتظر بایت مقصد *پیش از
`200`* می‌ماند و برای کلاینت‌های conforming قابل تحقق نیست؛ این نسخه منتظر همان بایت
*پس از `200`* می‌ماند و در این فاصله بایت‌های کلاینت را بافر/replay می‌کند. هیچ read
deadline روی تونل کاربر گذاشته نمی‌شود؛ انتظار با تعداد exitها بسته می‌شود، نه با یک
تایمر، تا تونل idle (SSH/WebSocket/long-poll) قطع نشود (§۳.۴).

بایت‌های اولیهٔ کلاینت (pipelined CONNECT) در handler خودمان buffer و بعد از reply
replay می‌شوند؛ `goproxy` این بایت‌ها را گم می‌کند و به همین دلیل رد شد.

### ۲.۳ خطاهای مقصد در VLESS/Trojan قابل انتساب نیستند

| علت | Dial | tracked error | مشاهدهٔ caller |
| --- | --- | --- | --- |
| خروجی dead / UUID غلط / tag غایب | nil | هیچ | EOF سریع |
| مقصد refused / NXDOMAIN / FIN / RST | nil | هیچ | EOF (بدون ECONNRESET) |
| خروجی TCP refused | nil | بله، ~۲٫۰s | closed pipe |
| deadline caller | nil | بله | closed pipe |

بنابراین `destination_unconfirmed` یک kind مستقل است و **نباید** به «خروجی خراب» یا «مقصد
refused» تبدیل شود؛ فقط شواهد مثبت (بایت برگشتی) یا خطای transport قابل انتساب است.
تنها استثنای ثبت‌شده نمونهٔ pre-byte یک CONNECT است (§۲.۲): همین kind برای «این exit
تونل را تا بایت اول نگه نداشت» به سلامت همان exit داده می‌شود، بدون هیچ ادعایی دربارهٔ
مقصد یا خرابی دائمی خروجی.

### ۲.۴ وضعیت pool

| وضعیت | تعریف |
| --- | --- |
| `healthy` | حداقل یک رکورد usable با `HealthHealthy` |
| `degraded` | هیچ healthy نیست، ولی حداقل یک رکورد usable با وضعیت `unknown` وجود دارد |
| `empty` | هیچ رکورد usable نیست (همه invalid/unsupported/cooling یا فهرست خالی) |

usable = وضعیت `healthy` یا `unknown` و cooldown منقضی. رکورد cooling تا پایان cooldown
قابل انتخاب نیست، مگر انتخاب دستی صریح.

### ۲.۵ ماتریس تصمیم

ستون «کاربر چه می‌بیند» همان چیزی است که `status`، لاگ و JSON نشان می‌دهند؛ هیچ سلولی
بی‌پاسخ نیست.

| # | حالت | کلاس | pool | مسیر | کاربر چه می‌بیند |
| --- | --- | --- | --- | --- | --- |
| ۱ | هدفمند | Self | هر | refuse | `403` / SOCKS `0x04`، دلیل `self_loop` |
| ۲ | هدفمند | Local | هر | direct | اتصال مستقیم؛ `status` = `local_exception` |
| ۳ | هدفمند | LAN | هر | direct | مستقیم؛ `lan_exception` |
| ۴ | هدفمند | Approved | healthy/degraded | pool | تونل؛ proof class در status/log |
| ۵ | هدفمند | Approved | empty | خطا | `pool_unavailable` قبل از reply؛ هیچ dial مستقیمی رخ نمی‌دهد |
| ۶ | هدفمند | Other | هر | direct | مستقیم؛ `unapproved` |
| ۷ | همه از پروکسی | Self | هر | refuse | `403` / `0x04` |
| ۸ | همه از پروکسی | Local | هر | direct | مستقیم؛ استثنای محلی در `status` و راهنما |
| ۹ | همه از پروکسی | LAN | هر | direct | مستقیم؛ `lan_exception` |
| ۱۰ | همه از پروکسی | Approved/Other | healthy/degraded | pool | تونل؛ اتصال‌های عمومی همیشه از pool |
| ۱۱ | همه از پروکسی | Approved/Other | empty | خطا | `pool_unavailable`؛ مقصد محلی همچنان کار می‌کند |
| ۱۲ | همه مستقیم | Self | هر | refuse | `403` / `0x04` |
| ۱۳ | همه مستقیم | بقیه | هر | direct | مستقیم؛ pool هیچ اثری ندارد و probe خودکار خاموش است |
| ۱۴ | متوقف | — | — | no listener | listener بسته است و engine/workerها متوقف‌اند؛ management socket باقی می‌ماند (`status`/`rules list`/`pool list` پاسخ می‌دهند)، اما `env`/`exec` و `pool test` exit `3`؛ اتصال ترافیک در سطح OS رد می‌شود؛ تصمیم مسیری وجود ندارد و خروج از این حالت process جدید می‌خواهد |

قاعدهٔ اضافی: انتخاب دستی خروجی (`pool select`) فقط سلول‌های pool را عوض می‌کند؛ اگر
خروجی انتخاب‌شده خراب باشد خطا می‌دهد و به exit دیگری سوییچ نمی‌کند (بی‌صدا نیست).
این تغییر `PolicyVersion` را بالا می‌برد (برای audit) ولی چون `Route` عوض نمی‌شود هیچ
اتصالی را نمی‌بندد. پین دستی در `status` به شکل `selected_exit` گزارش می‌شود؛ رشتهٔ
خالی یعنی انتخاب خودکار، پس پین دیگر یک حالت پنهان در فایل state نیست. در CONNECT، pin
پنجرهٔ failover پیش از بایت اول را هم خاموش می‌کند (§۲.۲).

### ۲.۶ تغییر حالت و قانون: چه چیزی بسته می‌شود

هر تغییر mode/rule یک `PolicyVersion` جدید می‌سازد و registry باید پیش از اعلام موفقیت
این sweep را کامل کند (D13):

| گذار | بسته می‌شود | نگه داشته می‌شود |
| --- | --- | --- |
| هدفمند → همه از پروکسی | همهٔ اتصال‌های direct به مقصد عمومی (Approved و Other) و همهٔ در حال dial | Local/LAN، Self-refused |
| همه از پروکسی → هدفمند | اتصال‌های pool به مقصد Other؛ اتصال‌های pool به Approved می‌مانند | direct Local/LAN، pool Approved |
| هر حالت → همه مستقیم | همهٔ اتصال‌های pool | اتصال‌های direct |
| همه مستقیم → هدفمند | اتصال‌های direct به مقصد Approved (و dialهای معلق آن‌ها) | Local/LAN و مقصد Other (همچنان direct) |
| همه مستقیم → همه از پروکسی | همهٔ اتصال‌های direct به مقصد عمومی Approved/Other | Local/LAN |
| هر حالت → متوقف | همه، و سرویس خارج می‌شود | — |
| افزودن قانون | اتصال‌های direct همان مقصد | بقیه |
| حذف قانون | اتصال‌های pool همان مقصد | بقیه |
| حذف/unsupported رکورد خروجی | اتصال‌های همان exit | اتصال‌های exitهای دیگر |
| rotate/rerank عادی | هیچ (اتصال زنده drain می‌شود) | همه |

اتصال‌های `unknown`/pending هم بسته می‌شوند: sweep شامل «در حال dial» است، نه فقط
جدول مسیر. warm poolهای HTTP و کانال‌های hijacked CONNECT بخشی از همین registry‌اند؛
تغییر جدول کافی نیست. یک CONNECT در پنجرهٔ pre-byte پس از `SetUpstream` وضعیت
`open` دارد؛ sweep آن را در `Closed` می‌شمارد (نه `PendingCancel`) و تلاش جاری (نه فقط
exit اولیه) را می‌بندد، چون `SetExit` با هر تلاش به‌روز می‌شود.

### ۲.۷ چرخهٔ عمر موتور

- یک `core.Instance` در هر process؛ **restart درون‌process ممنوع** (شاهد: gRPC/XHTTP
  پس از `core.Stop` وضعیت dialer سراسری و ۲→۵ goroutine جا می‌گذارند).
- tag هر exit **پایدار** است (`rl-<recordID>`): یک‌بار با اولین استفاده/افزودن رکورد
  ساخته می‌شود و فقط با تغییر تنظیمات رکورد (DedupeKey جدید) عوض می‌شود. mode/rule
  generation tag تازه نمی‌سازد؛ یک warm transport یک exit سالم می‌تواند برای dial بعدی
  همان exit استفاده شود، چون tag به exit اشاره دارد نه مقصد و frontend ما pool مشترک
  HTTP ندارد (forwarder بایت‌به‌بایت). ساختن tag per version یعنی افزودن/حذف handler
  در هسته به‌ازای هر تغییر، و `RemoveHandler` آن را نمی‌بندد؛ بنابراین T1 (ii) را به
  این شکل ساده‌تر و بدون نشتی پیاده می‌کنیم.
- انتخاب exit در Go است، نه balancer هسته؛ balancer خالی در T1 به default handler (و
  direct) رسید. `PolicyVersion` در هر درخواست dial به‌عنوان fence بررسی می‌شود: نسخهٔ
  قدیمی یا exit حذف/unsupportedشده پیش از هر ارسال رد می‌شود (tag غایب →
  `pool_unavailable`، نسخهٔ کهنه → `canceled`).
- مسیر direct یک dialer مستقل Go از نوع `contract.Dialer` است، نه outbound/tag هسته:
  `net.Dialer` با `DirectDialDeadline` و بررسی loopback/self-port پس از resolve. هیچ dial
  مسیر pool به default handler نمی‌رسد و outbound پیش‌فرض کانفیگ هرگز direct نیست؛
  بنابراین egress مستقیم از سلامت pool و کانفیگ موتور مستقل می‌ماند.
- حالت متوقف engine را می‌بندد و **خروج از حالت متوقف نیازمند process جدید است**؛
  `mode targeted` در حالت متوقف پیام «restart لازم است» می‌دهد. سرویس‌های supervisor
  این را در `T6` مدیریت می‌کنند.
- سوکت‌های warm بازماندهٔ gRPC/XHTTP تا پایان process زنده می‌مانند و در `status` به
  عنوان «transport‌های موتور (خارج از registry)» شمرده می‌شوند. این نقض D13 نیست چون
  هیچ تصمیم مقصدی در آن‌ها نیست (شاهد `spike/xray/FINDINGS.md` §5).
- keep/close در تغییر mode/rule: **keep** = transportهای warm رکوردهای هنوز usable و
  اتصال‌هایی که `Route`شان با نسخهٔ جدید یکی است؛ **close** = هر اتصالی که `Route`ش
  عوض شده، هر dial معلق با نسخهٔ قدیمی (مگر `keep` همان مسیر را تأیید کند)، و همهٔ
  اتصال‌های exit حذف/unsupportedشده.

## ۳. تاکسونومی خطا

### ۳.۱ kindها

`contract.ErrorKind` فقط این مجموعه است؛ هر خطا باید یکی را انتخاب کند و `path`
(`direct`/`pool`/`local`) را بگوید.

| kind | path | معنا |
| --- | --- | --- |
| `invalid_destination` | local | نرمال‌سازی/پورت نامعتبر |
| `unsupported` | local | UDP/BIND، ATYP ناشناس، transport حذف‌شده، عملیات خارج v1 |
| `policy_denied` | local | loop به خودمان، مقصد مدیریت، ممنوعیت محلی |
| `stopped` | local | سرویس listener ندارد |
| `pool_unavailable` | pool | exit سالم/tag پیش از هر ارسال وجود ندارد (D11) |
| `exit_unreachable` | pool | transport خروجی شکست (TCP/TLS/pin/tracked error) |
| `handshake_rejected` | pool | خروجی صریحاً درخواست/اعتبارنامه را رد کرد |
| `destination_refused` | pool | exit با `ProofConfirmed` گفت مقصد refused شد |
| `destination_unconfirmed` | pool | پذیرش شد، سپس EOF/سکوت بدون خطای transport |
| `timeout` | هر دو | deadline تمام شد |
| `canceled` | هر دو | ما لغو کردیم (کلاینت رفت، mode/stop/shutdown) |
| `connect_refused` | direct | مستقیم refused |
| `dns_failure` | direct | resolver محلی شکست خورد |
| `tls_failure` | direct | handshake/verification مستقیم یا probe |
| `source_fetch` | local | fetch/parse منبع شکست؛ pool قبلی حفظ می‌شود |
| `import_invalid` | local | رکورد در validate/sanitize رد شد |
| `state_corrupt` | local | فایل خوانده/مهاجرت نشد؛ fail closed |
| `state_locked` | local | writer دیگر مالک lock است |
| `peer_rejected` | local | SO_PEERCRED پذیرفته نشد |
| `not_found` | local | شیء نام‌برده‌شده (قانون، exit، منبع یا پیشنهاد) وجود ندارد؛ فقط نتیجهٔ فرمان، هرگز نتیجهٔ ترافیک |
| `external_tool_failed` | local | ابزار خارجی دسکتاپ/سرویس (`gsettings`، `kwriteconfig`/`kreadconfig`، `systemctl`) شکست خورد؛ هیچ نتیجه‌ای دربارهٔ اعمال/restore فرض نمی‌شود |
| `internal` | local | نقص نرم‌افزار؛ هرگز مبنای پیشنهاد نیست |

**پس از dial موفق و پیش از اولین بایت پاسخ.** اگر یک pool dial با proof
(`confirmed`/`accepted`) برگردد و سپس پیش از رسیدن اولین بایت پاسخ، **بدون خطای
transport قابل انتساب** شکست بخورد، kind همان `destination_unconfirmed` است؛ kind
جدیدی وجود ندارد. engine این حالت را از EOF/سکوت بدون tracked error می‌سازد و forwarder
HTTP نیز برای خطای stream بدون kind همین fallback را ثبت می‌کند. اگر tracked error،
timeout یا لغو وجود داشته باشد، kind خودش (`exit_unreachable`/`timeout`/`canceled`)
حفظ می‌شود و به unconfirmed تبدیل نمی‌شود. نگاشت §۳.۲ عوض نمی‌شود: تونلی که reply
گرفته است فقط بسته می‌شود؛ forwarder که هنوز پاسخی به کلاینت نداده، status خانوادهٔ
پیش از reply را می‌نویسد (`502`، یا `504` برای timeout).

**پنجرهٔ pre-byte در CONNECT.** در همین حالت، CONNECT یک تفاوت رویدادی دارد: پیش از
اولین بایت مقصد می‌توان همان بایت‌های کلاینت را به exit دیگری replay کرد (§۲.۲). kind
همچنان `destination_unconfirmed` است و هیچ ادعایی دربارهٔ مقصد نمی‌سازد، اما این نمونه
به‌عنوان «این exit تونل را تا بایت اول نگه نداشت» در سلامت همان exit ثبت و cooling
می‌کند؛ استثنای ثبت‌شده در §۶.۲ (مقصدی که خودش پیش از بایت اول می‌بندد) هم همین اثر
را دارد. اگر هر سه تلاش شکست بخورد، پاسخ دوم/صفحهٔ خطا نوشته نمی‌شود؛ فقط تونل بسته
می‌شود و هر تلاش جداگانه ثبت شده است.

### ۳.۲ نگاشت HTTP و SOCKS5

| kind | HTTP پیش از reply | SOCKS5 پیش از reply | پس از reply |
| --- | --- | --- | --- |
| `invalid_destination` | `400` | `0x04` | — |
| `unsupported` | `405`/`501` | `0x07` (BIND/UDP) یا `0x08` (ATYP) | — |
| `policy_denied` | `403` | `0x04` | — |
| `stopped` | `503` (best-effort) | — (listener نیست) | بستن |
| `pool_unavailable` | `502` | `0x04` | — |
| `exit_unreachable` | `502` | `0x04` | — |
| `handshake_rejected` | `502` | `0x04` | — |
| `destination_refused` | `502` | `0x04` (کد `0x05` فقط از refused واقعی لایهٔ مستقیم) | — |
| `destination_unconfirmed` | `502` فقط در forwarder پس از dial موفق و پیش از اولین بایت پاسخ؛ در CONNECT پیش از اولین بایت، پس از تلاش‌های bounded فقط بستن (۲۰۰ قبلاً رفته) | — | بستن؛ ثبت kind و cooling برای exit تلاش‌شده |
| `timeout` | `504` | `0x04` | بستن |
| `canceled` | `502` best-effort یا بدون reply | بدون reply | بستن |
| `connect_refused` | `502` | `0x05` | — |
| `dns_failure` | `502` | `0x04` | — |
| `tls_failure` | فقط در probe/فرمان‌ها | — | — |
| `source_fetch`، `import_invalid`، `state_corrupt`، `state_locked`، `peer_rejected`، `internal` | `500` با پیام redacted | `0x04` | — |
| `not_found`، `external_tool_failed` | `500` (فقط fallback؛ این kindها در ترافیک ساخته نمی‌شوند) | `0x04` | — |

**محدودیت ثبت‌شده (R-1):** `go-socks5 v0.1.4` کد reply را با substring از متن خطا
انتخاب می‌کند (`0x03` برای «network is unreachable»، `0x05` برای «refused»، بقیه
`0x04`) و `0x01`/`0x02` تولید نمی‌کند. قرارداد **کد سیم را** ثابت می‌کند: pool خانواده
`0x04`، direct refused `0x05`، BIND/UDP `0x07`، ATYP ناشناس `0x08`. برای رسیدن به
`0x05` متن خطا دستکاری نمی‌شود؛ فقط خطای واقعی refused از خود لایه می‌آید. تست‌های T3
همین کدها را assert می‌کنند، نه معنا را.

### ۳.۳ خط زمانی reply

1. تصمیم گرفته شد؛ اتصال در registry با `PolicyVersion` ثبت شد (اگر version قدیمی
   باشد `canceled`).
2. dial (direct یا pool adapter) با deadline مربوطه؛ مسیر core فاز dial قابل مشاهده ندارد
   و deadline آن در §۲.۱ توضیح داده شده است.
3. پیش از هر بایت reply: خطا → نگاشت جدول §۳.۲، سپس بستن.
4. `ProofConfirmed`/`ProofAccepted` → نوشتن `2xx`/`0x00` → replay بایت‌های buffered →
   copy دوطرفه. در pool CONNECT، بین `200` و اولین بایت مقصد یک پنجرهٔ replay هست:
   بایت‌های کلاینت buffer و به exit جاری فرستاده می‌شوند؛ مرگ pre-byte → تلاش بعدی
   (حداکثر ۳ exit) با همان بایت‌ها؛ اولین بایت مقصد یا پر شدن buffer → commit (ثبت
   exit/proof در registry) و copy عادی؛ پایان بودجه → بستن بدون reply دوم.
5. خطا پس از commit/reply: هیچ reply دوم؛ `Close` روی هر دو سوکت و ثبت kind. کلاینت EOF
   می‌بیند، نه صفحهٔ خطا.

### ۳.۴ اعداد و آستانه‌ها

| مورد | مقدار | مبنا |
| --- | --- | --- |
| `PoolDialDeadline` | ۱۰s | فقط handshake مسدود adapter SOCKS5؛ مسیر core فاز dial قابل مشاهده ندارد (§۲.۱) |
| `ConnectFailoverExits` | ۳ (۲ retry) | سقف تلاش exit در یک CONNECT پیش از اولین بایت مقصد؛ تصمیم مهندسی برای شواهد ۴۰–۵۰٪ خرابی exit |
| `ConnectReplayBufferBytes` | ۶۴KiB | سقف بایت‌های کلاینت برای replay؛ رسیدن به سقف پنجره را می‌بندد و exit جاری را commit می‌کند |
| `DirectDialDeadline` | ۱۰s | — |
| `ProbeTimeout` | ۲۰s | dial از exit ≤۱۰s + انتظار اولین بایت ≤۸s |
| `ProbeFirstByte` | ۸s | فقط برای egress probe |
| بستن سمت ما (`LocalCloseDeadline`) | **p99 ≤ ۵۰ms** | اندازه‌گیری ۲٫۴–۳٫۰ms |
| لغو dial معلق | **p99 ≤ ۵۰ms** | ۲٫۳۴–۲٫۸۰ms |
| مشاهدهٔ مقصد از سمت exit، سیاست پیش‌فرض | **≤ ۱٫۵s** (اطلاعاتی) | ۱٫۰۰۱–۱٫۰۰۳s |
| مشاهدهٔ مقصد، سیاست سریع exit | **≤ ۲۰ms** (bound آزمون، اطلاعاتی) | ۲٫۴۶–۳٫۰۷ms |
| نبود اتصال/goroutine باقی‌مانده از مسیر VLESS/RAW | baseline | `spike/xray/FINDINGS.md` §4 |
| نبود درخواست جدید با version قدیمی پس از sweep | صفر | counter در T3 |

دو ردیف «اطلاعاتی» **تضمین ما نیستند**: سیاست ~۱s خروجی عمومی (`uplinkOnly/downlinkOnly`)
خارج از کنترل ماست. تضمین ما بستن فوری سمت خودمان و لغو dial است؛ مشاهدهٔ سمت مقصد به
سیاست exit بستگی دارد و در `status`/docs همین‌طور گفته می‌شود.

**هیچ read deadline روی تونل کاربر گذاشته نمی‌شود.** `ProbeFirstByte` فقط به probe
خروجی تعلق دارد؛ اتصال idle (SSH، WebSocket، long-poll) با ۱۵ یا ۸ ثانیه قطع نمی‌شود.
پنجرهٔ pre-byte CONNECT هم تایمر ندارد: انتظار برای بایت مقصد با تعداد exit (۳) و
سقف buffer بسته می‌شود، نه با یک deadline، چون کلاینتی که هنوز بایتی نفرستاده ممکن
است یک تونل idle باز کرده باشد؛ مقصدی که ساکت می‌ماند نامتعهد می‌ماند تا یکی از
رویدادهای پایان پنجره رخ دهد (§۲.۲، R-15). `~۵۰۲ms/۱۰۳ms` فقط برای انتخاب آستانهٔ
probe استفاده شد.

### ۳.۵ اهلیت پیشنهاد تشخیصی (D09)

| شاهد | مجاز به ساخت پیشنهاد؟ |
| --- | --- |
| `connect_refused` روی direct | بله |
| `timeout` روی direct | بله |
| `dns_failure` روی direct | بله |
| هر خطای pool (همهٔ kindهای pool) | نه |
| `tls_failure` (گواهی/handshake نامعتبر) | نه |
| HTTP status (۴۰۳/۴۵۱/۵۰۳…)، CAPTCHA، rate-limit، بدنه | نه — این‌ها اصلاً وارد تاکسونومی نمی‌شوند؛ forwarder آن‌ها را بایت‌به‌بایت عبور می‌دهد |
| `canceled`، تغییر mode، `stopped`، EOF عادی | نه |
| `internal`، و هر kind محلی/عملیاتی (`invalid_destination`، `policy_denied`، `source_fetch`، `import_invalid`، `state_*`، `peer_rejected`) | نه |

پیشنهاد فقط از `ProbeResult.Eligible == true` ساخته می‌شود و فقط در حافظهٔ registry
سرویس زنده می‌ماند؛ پرچم در `prober` از همین جدول محاسبه می‌شود. پذیرش یک پیشنهاد
`route` یک `Rule` با `Origin=user` می‌سازد و پذیرش یک پیشنهاد `unroute` همان قانون
نام‌برده‌شده را پس از تأیید صریح حذف می‌کند؛ هیچ‌کدام بدون تصمیم کاربر رخ نمی‌دهد.

## ۴. state و config

### ۴.۱ مسیرها و مجوزها

| داده | مسیر پیش‌فرض | مجوز |
| --- | --- | --- |
| config | `$XDG_CONFIG_HOME/routelatch/config.json` (پیش‌فرض `~/.config/...`) | پوشه `0700`، فایل `0600` |
| state | `$XDG_STATE_HOME/routelatch/state.json` (پیش‌فرض `~/.local/state/...`) | `0700`/`0600` |
| نسخهٔ قبلی | `state.json.bak` در همان پوشه | `0600` |
| backupها | `$XDG_STATE_HOME/routelatch/backup/state-<UTC>.json` | `0600` |
| lock | `<InstanceDir>/state.lock`، با `InstanceDir = $XDG_STATE_HOME/routelatch/run` | `0600` |
| management socket | `<InstanceDir>/control.sock` | پوشه `0700`، socket `0600` |
| app lock/owner | `<InstanceDir>/app.lock` و `<InstanceDir>/app.owner` (D26) | `0600` |
| log | `$XDG_STATE_HOME/routelatch/log/routelatch.log` | پوشه `0700`، فایل `0600` (append-only) |

> **وضعیت:** R12 فعلی بسته به محیط سه چیدمان دارد: با `XDG_RUNTIME_DIR`، lock در
> `$XDG_RUNTIME_DIR/routelatch/state.lock` و socket در همان پوشه؛ بدون آن، lock در
> `<StateDir>/state.lock` و socket در `<StateDir>/run/control.sock`؛ و با
> `--data-dir <abs>`، lock در `<abs>/state.lock` و socket در `<abs>/run/control.sock`.
> D26/R13 همه را به `InstanceDir` مشتق از state/data
> (`<StateDir>/run` یا `<data-dir>/run`) منتقل می‌کند تا desktop و SSH با
> `XDG_RUNTIME_DIR` متفاوت همان lock/socket را ببینند. این migration آگاهانه در
> R13-B انجام می‌شود و در پنجرهٔ گذار، lockهای legacy هم probe/نگه داشته می‌شوند
> (§۷.۵).

**لاگ.** فایل فقط append می‌شود، در پوشهٔ `0700` با فایل `0600` ساخته می‌شود، از
`log_level` پیروی می‌کند و در ۴ MiB می‌چرخد و سه فایل چرخیده (`routelatch.log.1`–`.3`)
را نگه می‌دارد. هیچ URL، credential، بدنهٔ درخواست یا query string در آن نوشته نمی‌شود.
اگر مسیر لاگ قابل نوشتن نباشد، سرویس با یک warning غیرکشنده ادامه می‌دهد و `doctor`
مسیر resolved و وضعیتش را نشان می‌دهد؛ چون یک تسهیلات تشخیصی نباید به denial of service
تبدیل شود.

`--data-dir <abs>` همه را زیر یک ریشه می‌چیند (برای تست‌های ایزوله). مسیر socket اگر از
سقف طول Unix (~۱۰۸ بایت) بگذرد، خطای روشن با پیشنهاد state/data کوتاه‌تر یا
`--data-dir` کوتاه‌تر.

اگر پوشهٔ state مجوز گروه/دیگران داشته باشد یا فایل `0600` نباشد، سرویس **fail closed**
می‌کند (`state_corrupt`/`policy_denied` با راهنمای `chmod`) و `doctor` وضعیت را نشان
می‌دهد؛ نوشتن credential در فایل عمومی مجاز نیست. `doctor` دستور fix پیشنهادی را نشان
می‌دهد، ولی اصلاح مجوز فقط با فرمان صریح کاربر (مثلاً `chmod`) انجام می‌شود.

### ۴.۲ `config.json`

```json
{
  "schema_version": 1,
  "http_listen": "127.0.0.1:8317",
  "socks_listen": "127.0.0.1:8318",
  "log_level": "info",
  "tun_enabled": false,
  "diagnostics": { "enabled": true, "queue_limit": 32, "max_concurrent": 1, "retention_days": 30 },
  "probes": { "targets": [], "timeout_ms": 20000, "cooldown_base": 60, "cooldown_max": 900 }
}
```

`tun_enabled` یک فیلد additive با پیش‌فرض `false` است و فقط «خواست کاربر» برای
پوشش TUN را نگه می‌دارد، نه اثبات فعال‌بودن آن؛ جزئیات در §۱۰.۲.

هر دو آدرس باید loopback باشند (`127.0.0.1` یا `::1`)؛ هر چیز دیگر `unsupported` با
exit `7` است (D14)، نه usage/exit `2` و نه `policy_denied`. تغییر آدرس listener فقط با
restart؛ سرویس درجا rebind نمی‌کند.

listenerهای ترافیک احراز هویت ندارند و هیچ‌گاه نباید «امن» یا «فقط برای من» توصیف
شوند: هر فرایند هم‌کاربر می‌تواند از pool استفاده کند (D14). حفاظت management کاملاً
جدا و در §۴.۶ است.

### ۴.۳ `state.json`

```json
{
  "schema_version": 1,
  "policy_version": 7,
  "mode": "targeted",
  "selected_exit": "",
  "rules": [{ "id": "r-...", "kind": "domain_suffix", "host": "example.com", "origin": "user", "added_at": "..." }],
  "sources": [{ "id": "s-...", "kind": "subscription", "label": "...", "order": 0, "refreshed_at": "..." }],
  "records": [{ "id": "x-...", "dedupe_key": "sha256:...", "source_ids": ["s-..."], "protocol": "vless", "server": "...", "port": 443, "transport": "xhttp", "security": "tls", "status": "unknown" }],
  "health": [{ "exit": "x-...", "status": "healthy", "ewma_latency_ms": 210, "samples": 4 }],
  "desktop": { "applied": false, "environment": "", "instance_id": "", "keys": [] },
  "updated_at": "..."
}
```

فیلدهای secret (UUID/password، URL منبع) در همین فایل `0600` هستند و در هیچ خروجی
CLI/JSON/لاگ بازتولید نمی‌شوند. `status` فهرست همیشه `dedupe_key` را کوتاه (۸ کاراکتر
اول) یا حذف می‌کند.

`state.json` هیچ فیلد `proposals` ندارد: پیشنهادها و شواهدشان فقط در حافظهٔ registry
سرویس زنده‌اند و با restart از بین می‌روند (D21). `proposals list` نمای همان حافظه است،
نه تاریخ، و TTL ۳۰ روزه فقط یک سقف حافظه است. نوشتن‌های این build در
state/config/backup/log هیچ مقصد دیده‌شده‌ای را نگه نمی‌دارند. state قدیمی‌تر که فیلد
`proposals` دارد تا اولین write روی دیسک می‌ماند؛ در decode نادیده گرفته می‌شود، نوشتن
بعدی حذفش می‌کند و `.bak` هنگام کپی از آن پاک می‌شود.

### ۴.۴ نوشتن اتمیک و یک writer

`Store.Mutate` دقیقاً این ترتیب را اجرا می‌کند:

1. `flock(LOCK_EX|LOCK_NB)` روی فایل lock؛ شکست → `state_locked`.
2. خواندن، مهاجرت schema در حافظه، اجرای callback روی کپی.
3. اگر mode/rules عوض شده و `policy_version` بزرگ نشده → خطا (بدون نوشتن).
4. نوشتن `state.json.tmp-<pid>` با `O_CREATE|O_EXCL`، مجوز `0600`.
5. `f.Sync()`، `f.Close()`.
6. کپی محتوای قبلی به `state.json.bak` (best-effort، فقط اگر نسخهٔ قبلی سالم بود؛
   پیشنهادهای legacy در صورت قابل decode بودن حذف می‌شوند).
7. `os.Rename(tmp, state.json)`، سپس `fsync` روی پوشه.
8. آزادکردن lock؛ بازگشت snapshot جدید.

`Store.MutateConfig` همین الگوریتم را روی `config.json` اجرا می‌کند؛ این دو فایل جدا
هستند و اتمیک‌بودن هم‌زمانشان وعده داده نمی‌شود. تغییر listener فقط پس از restart اثر
می‌کند.

هیچ کامپوننتی جز `Store` فایل را نمی‌نویسد. CLI وقتی سرویس بالاست از management socket
می‌خواهد؛ وقتی سرویس خاموش است خودش lock را می‌گیرد (offline mode). دو writer هم‌زمان
وجود ندارد؛ lock با مرگ process آزاد می‌شود.

### ۴.۵ schema، مهاجرت، backup و restore

- `schema_version` عدد صحیح است؛ نسخهٔ فعلی `1`.
- نسخهٔ قدیمی‌تر: پیش از مهاجرت backup اجباری، مهاجرت‌ها تابع خالص و تست‌پذیر، سپس
  نوشتن اتمیک. مهاجرت نیمه‌کاره هرگز commit نمی‌شود.
- نسخهٔ جدیدتر از build: `state_corrupt` با پیام «با نسخهٔ جدیدتر ساخته شده»؛ **هیچ
  downgrade یا حدس ساختاری**.
- فایل خراب: fail closed؛ سرویس بالا نمی‌آید و فایل را بازنویسی نمی‌کند. `restore` فقط
  با backup معتبر (schema + checksum) و پس از تأیید کاربر.
- `backup` یک envelope می‌نویسد: `{"schema_version","created_at","checksum":"sha256:...","state":{...}}`
  که checksum روی بایت‌های canonical `state` است. `restore` checksum، schema و اینکه
  مسیر داخل پوشهٔ backup بماند (بدون `..`) را بررسی می‌کند؛ سپس از فایل فعلی backup
  می‌گیرد و جایگزین می‌کند. backup ناقص یا دستکاری‌شده رد می‌شود.
- backup ساخته‌شده با build قدیمی‌تر ممکن است در payload خود تاریخچهٔ پیشنهاد داشته
  باشد؛ checksum آن با همان شکل قدیمی بررسی می‌شود، اما پیشنهادها و شواهد در restore
  نادیده گرفته می‌شوند و وارد state زنده نمی‌شوند (D21). backupهای جدید هیچ فیلد
  `proposals` ندارند. فایل‌های backup قدیمی روی دیسک تا حذف دستی همان تاریخچه را
  نگه می‌دارند؛ RouteLatch آن‌ها را بازنویسی نمی‌کند.

### ۴.۶ management socket

- Unix stream socket، بدون TCP و بدون HTTP management. فریم = یک JSON در هر خط،
  پاسخ = یک `Envelope` در هر خط. سقف درخواست `MaxManagementRequest = 64 KiB` و سقف
  پاسخ `MaxManagementResponse = 4 MiB` (فهرست ۲۰۰۰ رکورد جا می‌شود)؛ `pool list`
  `--limit/--offset` هم دارد.
- هر درخواست `schema_version` دارد و باید `1` باشد؛ ناشناخته → `unsupported`.
- پس از `bind`، `chmod 0600` و `stat` تأیید می‌شود (مستقل از umask).
- برای هر اتصال `SO_PEERCRED` خوانده می‌شود؛ `uid` باید برابر `os.Getuid()` باشد،
  وگرنه بستن + `peer_rejected` (فقط uid لاگ می‌شود، نه دادهٔ درخواست). root و
  هم‌uid خارج از مرز امنیتی ما هستند و همین‌طور مستند می‌شود.
- deadline هر درخواست ۵s است و هر فرمان بودجهٔ ثابت خودش را دارد: `pool test` ۱۰ دقیقه
  (بدون فرمول وابسته به تعداد exit؛ فهرست با `MaxPoolTestExits = 20` بریده می‌شود)،
  `diagnose` و `proposals recheck` ۶۰s، `pool refresh` ۹۰s، `import`/`pool add` ۶۰s، و
  `desktop set|restore`/`backup`/`restore` ۳۰s. بیشترین بودجه همان ۱۰ دقیقه است و
  deadline سوکت برابر timeout + ۲s؛ در `pool test` اگر probe خطا بدهد یا deadline برسد،
  نتایج partial با `truncated=true` برمی‌گردد. درخواست بدون پاسخ هرگز state را نیمه‌کاره
  رها نمی‌کند (mutation اتمیک است).
- پاسخ هیچ secret، URL منبع یا share link ندارد.

### ۴.۷ singleton و تصادم

- مالکیت = گرفتن `state.lock` + bind موفق socket. نمونهٔ دوم با exit `4` و پیام
  «RouteLatch دیگری مالک این data dir است» خارج می‌شود و هیچ فایلی را دست نمی‌زند.
- socket کهنه فقط وقتی حذف می‌شود که lock را داریم و `connect` با `ECONNREFUSED`
  شکست بخورد.
- اگر پورت listener اشغال باشد: خروجی `6` با نام پورت و پیام «هیچ ترافیکی تغییر مسیر
  نداد»؛ هیچ‌گاه پورت جایگزین بی‌صدا انتخاب نمی‌شود و desktop proxy تنظیم نمی‌شود.
- `desktop` snapshot در state نگه داشته می‌شود تا `restore` آفلاین هم کار کند؛
  `Applied=true` با listener مرده در `doctor` دیده و فرمان restore پیشنهاد می‌شود.

### ۴.۸ ماتریس دسکتاپ (v1)

| محیط | پشتیبانی | ابزار | رفتار |
| --- | --- | --- | --- |
| GNOME | بله | `gsettings` (`org.gnome.system.proxy`، `ignore-hosts`) | set/restore با ownership check |
| KDE | بله | `kwriteconfig6`/`kwriteconfig5` (`kioslaverc`) | set/restore با ownership check |
| سایر | نه | — | exit `7` و پیام صریح «این محیط پشتیبانی نمی‌شود»؛ هرگز ادعای «پروکسی کل سیستم» |

شکست خود ابزار با `external_tool_failed` (exit `1`) گزارش می‌شود، نه با «محیط پشتیبانی
نمی‌شود»: `gsettings`/`kwriteconfig`/`kreadconfig` (و در T6 `systemctl`) اگر خطا بدهند
هیچ نتیجه‌ای دربارهٔ اعمال یا restore فرض نمی‌شود و rollback همان مسیر `Apply` اجرا
می‌شود. نبود ابزار یا محیط پشتیبانی‌نشده `unsupported` (exit `7`)، سرویس ناسالم
`stopped` (exit `3`)، marker ناسازگار/خراب `state_corrupt` (exit `10`)، timeout `8`،
لغو `12` و تعارض/قفل state `6` است.

restore **کلید‌به‌کلید** است: `DesktopProxyState.Keys` برای هر کلید مقدار نوشته‌شده و
مقدار قبلی را نگه می‌دارد و فقط کلیدی برگردانده می‌شود که مقدار فعلی‌اش هنوز همان مقدار
ماست. ویرایش کاربر روی کلیدهای دیگر restore را خراب نمی‌کند. حالت بدون listener هرگز
desktop را مستقیم نمی‌کند؛ `doctor` و `desktop restore` آفلاین در دسترس‌اند.

`set` وقتی marker `applied` و تمام کلیدها هنوز همان مقدار ثبت‌شده را دارند، no-op است
و marker را عوض نمی‌کند. اگر کاربر بعد از apply کلیدی را دستی تغییر داده باشد، اجرای
صریح `desktop proxy set` مجوز بازپس‌گیری مالکیت است: فقط کلیدهایی که کاربر تغییر داده
به‌عنوان `Previous` نقطهٔ restore تازه می‌شوند و بقیهٔ کلیدها `Previous` اصلی خود را حفظ
می‌کنند؛ وگرنه restore هیچ مقداری برای برگرداندن آن کلیدها نداشت. marker جدید قبل از نوشتن
ذخیره و سپس مقادیر هدف دوباره اعمال می‌شوند. بنابراین `restore` بعدی کلید ویرایش‌شده را به
آخرین مقدار دستی کاربر و بقیهٔ کلیدها را به مقدار پیش از اولین apply برمی‌گرداند. اگر خواندن،
اعتبارسنجی مقدار فعلی یا ذخیرهٔ marker شکست بخورد، هیچ کلیدی نوشته نمی‌شود. تغییر محیط دسکتاپ
یا مقادیر listenerهای ثبت‌شده همچنان مستلزم `restore` دستی است.

کلیدهای مدیریت‌شدهٔ KDE همان شش کلید `ReversedException`، `httpProxy`، `httpsProxy`،
`socksProxy`، `NoProxyFor` و `ProxyType` هستند؛ `ftpProxy` و `Authmode` دست‌نخورده
می‌مانند. در GNOME فقط `http`/`https`/`socks` (`host` و `port`)، `ignore-hosts` و
`mode` نوشته می‌شوند؛ `ftp`، `use-same-proxy`، `autoconfig-url` و کلیدهای
authentication دست‌نخورده‌اند.

`desktop proxy status` در حالت `applied` همان کلیدهای ثبت‌شده را دوباره از OS می‌خواند
و کلیدهایی را که کاربر بعد از apply عوض کرده در `conflict` (نام کلیدها) و `notice`
گزارش می‌کند؛ بنابراین marker کهنه به‌عنوان مالکیت بی‌قید نمایش داده نمی‌شود. اگر خواندن
ممکن نباشد، `notice` می‌گوید مالکیت قابل تأیید نیست و هیچ نتیجهٔ تازه‌ای فرض نمی‌شود.
تشخیص محیط فقط از متغیرهای نشست است (`XDG_CURRENT_DESKTOP`، `DESKTOP_SESSION`،
`KDE_FULL_SESSION`، `GNOME_DESKTOP_SESSION_ID`) و هیچ‌وقت حدس نمی‌زند: سرویس
`systemd --user` این متغیرها را از نشست گرافیکی دارد، ولی اجرای دستی
`desktop proxy set` از پوسته‌ای که هیچ‌کدام را ندارد `unsupported` (exit `7`) می‌دهد؛
`desktop proxy status` در همان محیط با `supported:false` و exit `0` برمی‌گردد و
هیچ تنظیمی را تغییر نمی‌دهد.

## ۵. pool، import و sanitization

### ۵.۱ هویت رکورد

`DedupeKey = "sha256:" + hex(sha256(canonical))` با canonical شامل این فیلدها و **بدون**
نام نمایشی، منبع، ترتیب یا مقدارهایی که default شده‌اند:

`protocol`, `lower(server)`, `port`, `transport`, `security`, `sni`, `alpn` (مرتب)،
`fingerprint`, `flow`, `path`, `host`, `grpc service`, `pinnedPeerCertSha256`,
`verifyPeerCertByName`، canonical `xhttp extra` (کلیدها
مرتب، مقدار خالی حذف)، و `auth = sha256(credential bytes)`. فیلدهای خالی/default قبل از
hash حذف می‌شوند تا دو لینک معادل یک key بدهند. `server` در import به `Destination`
نرمال می‌شود؛ نامعتبر → رکورد `invalid`.

`SourceID = "s-" + ۱۲ کاراکتر اول sha256(kind + "\x00" + canonical origin)` است؛
هیچ‌گاه URL خام یا label نیست، چون ID در status/warning/لاگ دیده می‌شود و ممکن است
token داشته باشد.

پیامد: دو لینک با نام متفاوت ولی تنظیم یکسان یکی می‌شوند؛ دو لینک با UUID متفاوت دو
exit جدا هستند. `RecordID` از `DedupeKey` مشتق می‌شود و با تغییر تنظیمات عوض می‌شود.
هویت (`DedupeKey`/`RecordID`)، مالکیت منبع (`SourceIDs`) و سلامت (`ExitHealth`) سه
دادهٔ جدا هستند و هیچ‌کدام از دیگری مشتق نمی‌شود.

### ۵.۲ منبع، مالکیت و refresh

- هر رکورد فهرست `SourceIDs` دارد. یک لینک مشترک در دو اشتراک یک رکورد با دو منبع است؛
  refresh یکی، دیگری را حذف نمی‌کند.
- حذف منبع فقط ارجاع‌هایش را برمی‌دارد؛ رکورد بی‌ارجاع حذف و اتصال‌های زندهٔ همان exit
  با `CloseExitGone` بسته می‌شوند.
- ترتیب ورودی کاربر حفظ می‌شود؛ refresh ترتیب را عوض نمی‌کند.
- **هیچ fetch خودکار شبکه‌ای وجود ندارد**: startup از pool ذخیره‌شده استفاده می‌کند و
  فقط `pool refresh` صریح (یا `import` جدید) شبکه را لمس می‌کند. وقتی pool خالی/خراب
  است، `status` فرمان دقیق refresh را نشان می‌دهد.

### ۵.۳ وضعیت‌ها و گذارها

| از | به | علت |
| --- | --- | --- |
| جدید | `unknown` | معتبر، بدون سنجش |
| جدید | `unsupported` | دادهٔ ناقص/ممنوع، پروتکل/transport خارج v1، `security` پشتیبانی‌نشده، یا `insecure_tls_removed` |
| هر (به‌جز `invalid`) | `unsupported` | engine نتوانست رکورد را به exit بسازد؛ `StatusReason = engine_record_rejected` |
| جدید | `invalid` | دادهٔ اتصال قابل نرمال‌سازی/اعتبارسنجی نیست |
| `unknown` | `healthy` | شاهد مثبت egress: بایت برگشتی در استفادهٔ واقعی یا probe هدف‌دار |
| `unknown`/`healthy` | `cooling` | خطای transport قابل انتساب؛ `cooldown_until` تنظیم می‌شود |
| `cooling` | `unknown` | probe transport موفق؛ cooldown برداشته می‌شود ولی healthy ادعا نمی‌شود |
| `cooling` | `healthy` | شاهد مثبت egress؛ شمارندهٔ شکست صفر |
| `cooling` | `cooling` | شکست مجدد؛ cooldown نمایی تا سقف |
| هر | `invalid` | بازاعتبارسنجی دادهٔ اتصال را شکست داد |
| رکورد حذف/غیرفعال | حذف | اتصال‌های همان exit بسته می‌شوند |

`invalid`/`unsupported` برای همان نسخهٔ رکورد نهایی‌اند؛ refresh با دادهٔ درست یک
`DedupeKey` جدید می‌سازد. `destination_unconfirmed` در probe یا stream معمولی هیچ‌گاه
رکورد را cooling نمی‌کند؛ تنها استثنا **مرگ pre-byte یک CONNECT پس از proof** است که
یک نمونهٔ شکست برای همان exit ثبت می‌کند (§۶.۲) و همچنان ادعای مقصد نمی‌سازد.

**رکوردی که engine نمی‌تواند بسازد startup را متوقف نمی‌کند.** اگر یک رکورد از
validate/config engine رد شود، از فهرست dial خارج می‌شود، حذف نمی‌شود و (اگر از قبل
`invalid` نباشد) در state به `unsupported` با `StatusReason = engine_record_rejected`
تغییر می‌کند؛ فقط خطای سراسری config/هسته startup را متوقف می‌کند. `pool list` (JSON)
این رکورد را با `status` و `status_reason` نشان می‌دهد و `status` آن را در شمار
unsupported/quarantined هشدار می‌دهد. حذف آن فقط با فرمان معمول `pool remove <id>` از
طریق management socket است؛ v1 هیچ فرمان حذف آفلاین ندارد، چون startup دیگر به یک
رکورد خراب وابسته نیست.

**وضعیت مشتق‌شده برای رکورد قدیمی یا دست‌ویرایش‌شده.** state قدیمی یا ویرایش دستی
می‌تواند برای رکوردی که v1 نمی‌تواند dial کند هر statusی داشته باشد. نمای pool
هنگام خواندن و گزارش، وضعیت را مشتق می‌کند: `http` → `unsupported` با
`StatusReason = http_exit_not_verified`، هر پروتکل غیرخالی خارج از
`{vless, trojan, socks5, hysteria2, http}` → `unsupported` با
`StatusReason = protocol_unsupported`، و یک رکورد ذخیره‌شدهٔ `hysteria2` با
`obfs = gecko` و رمز obfs خالی (که پیش از دروازهٔ import وارد شده) →
`unsupported` با `StatusReason = missing_obfs_password`. این حکم مشتق‌شده بر **هر**
status ذخیره‌شده
اولویت دارد — از جمله `unknown`/فیلد خالی، `cooling` (با cooldown منقضی یا فعال)،
`healthy`، و `unsupported` با دلیل کهنه مثل `engine_record_rejected` یا
`sidecar_unavailable` — و تنها استثنا
`invalid` است که وضعیت ایمنی سخت‌گیرانه‌تری است و همان `invalid` می‌ماند. برای
پروتکل‌های پشتیبانی‌شده (`vless`/`trojan`/`socks5`/`hysteria2`) با تنظیمات قابل dial
هیچ‌چیز تغییر نمی‌کند و statusهای
ذخیره‌شده همان معنای قبلی را دارند. این کار **فقط در view** انجام می‌شود و state را
بازنویسی نمی‌کند: `pool list`، شمارندهٔ هشدار `status` و `doctor` همگی همین وضعیت را
می‌شمارند، رکورد از شمار `unknown`/`healthy`/`cooling` خارج و در شمار `unsupported` می‌آید، و
رکورد هرگز dial یا probe نمی‌شود. رکورد با پروتکل خالی رفتار فعلی خود را نگه می‌دارد.

**VLESS/Trojan بدون TLS به exit عمومی.** `validateRecord` هستهٔ pin‌شده این حالت را رد
می‌کند: اگر protocol غیر از SOCKS5 باشد، `security` برابر `tls` نباشد و `publicServer`
سرور را عمومی بداند (IP غیر `private`/`loopback`/`link-local`، یا دامنه‌ای که در
private-domain list هسته نیست)، رکورد ساخته نمی‌شود. این قاعدهٔ خود هستهٔ pin‌شده است، نه
سیاست مسیریابی RouteLatch؛ SOCKS5 معاف است چون adapter خودمان آن را dial می‌کند و
loopback/private/غیرعمومی مجاز می‌مانند. نتیجه در state `unsupported` با
`StatusReason = engine_record_rejected` است: `pool list` (JSON) دلیل را در
`status_reason` نشان می‌دهد و `doctor` رکورد را در شمار unsupported/quarantined هشدار
می‌دهد و به `pool list` ارجاع می‌دهد.

### ۵.۴ sanitization ورودی

رکورد فقط از allow-list ساخته می‌شود؛ کانفیگ خام هرگز اجرا نمی‌شود.

**پذیرفته:** پروتکل `vless`/`trojan`/`socks5`؛ transport `raw`/`tls`/`ws`/`grpc`/`xhttp`؛
security `none`/`tls`؛ آدرس، پورت، UUID/password، `sni`, `alpn`, `fingerprint`, `flow`،
مسیر/Host برای ws/xhttp، `serviceName` برای gRPC، `pinnedPeerCertSha256`،
`verifyPeerCertByName`، و `extra` XHTTP با allow-list زیر.
`http` و `reality` شناخته می‌شوند ولی به‌صورت `unsupported` وارد می‌شوند (نه حذف
بی‌صدا)؛ `reality` خارج از v1 است و پشتیبانی نمی‌شود (`ReasonRealityUnverified`).
هر `http://`/`https://` با دلیل
`StatusReason = http_exit_not_verified` و هر scheme ناشناس با
`StatusReason = protocol_unsupported` ذخیره می‌شود؛ import هرگز رکورد `unknown` برای
این پروتکل‌ها نمی‌سازد. هر رکورد قدیمی/دست‌ویرایش‌شده‌ای با این پروتکل‌ها، مستقل از
status ذخیره‌شده (به‌جز `invalid`)، در view مشتق می‌شود (§۵.۳) و state آن بازنویسی
نمی‌شود.

**حذف‌شده و شمارش‌شده (بدون اجرا):** کلیدهای ریشه‌ای `routing`, `dns`, `api`, `env`,
`inbounds`, `observatory`, `policy`, `stats`, `reverse`, `fakedns`, `log`؛ `mux` (v1
بدون mux؛ حذف با پیام)؛ هر `*File`/مسیر فایل محلی؛ هر `*_script`/`*Script`؛
`headers` تکراری/بزرگ؛ کلیدهای ناشناخته.

**رد رکورد (نه فقط حذف):** `proxySettings` یا `sockopt.dialerProxy` (وابستگی به outbound
دیگر)، `balancerTag`، `tlsSettings.certificates[].certificateFile` یا هر مسیر فایل، عمق JSON
> ۸، کلید > ۱۲۸، مقدار رشته‌ای > ۸ KiB، کل کانفیگ > ۲۵۶ KiB. ورودی
`allowInsecure`/`insecure` رکورد را `unsupported` با
`StatusReason = "insecure_tls_removed"` می‌کند؛ هیچ مسیر پذیرش/رضایتی وجود ندارد و
رکورد هرگز dial نمی‌شود (D18، R-12). اگر همان لینک `pinnedPeerCertSha256` هم دارد،
pin اعمال می‌شود، verification روشن می‌ماند و رکورد پذیرفته می‌شود؛ نادیده‌گرفتن
`allowInsecure` در import notice گزارش می‌شود.
در عوض `pinnedPeerCertSha256` و `verifyPeerCertByName` در allow-list هستند: **pin/name
constraint با verification روشن**، نه استثنای ناامن.

**allow-list `extra` XHTTP** (نام فیلدها از هستهٔ pin‌شده): `host`, `path`, `mode`,
`headers` (≤۱۶ جفت، نام token معتبر، بدون CR/LF)، `xPaddingBytes`, `noGRPCHeader`,
`noSSEHeader`, `scMaxEachPostBytes`, `scMinPostsIntervalMs`, `scMaxBufferedPosts`,
`scStreamUpServerSecs`, `xmux` (فقط `maxConcurrency`, `maxConnections`, `cMaxReuseTimes`,
`hMaxRequestTimes`, `hMaxReusableSecs`, `hKeepAlivePeriod`). `downloadSettings` و
`h2Params` و هر کلید ناشناس رد می‌شوند: `downloadSettings` یک StreamConfig کامل با
security/sockopt خودش است. موفقیت config builder شاهد sandbox نیست.

**obfs هیستریا۲:** `obfs` اختیاری است و نبودنش رکورد را تغییر نمی‌دهد. از دو نوع
پذیرفته‌شده، `salamander` و `gecko`، هر دو به `obfs-password` غیرخالی نیاز دارند؛
`gecko` بدون رمز یا با رمز خالی همان‌جا در import با `invalid` و
`StatusReason = missing_obfs_password` رد می‌شود، چون sing-box v1.14.4 کانفیگ
hysteria2 را با رمز خالی obfs با خطای «missing obfs password» رد می‌کند و چنین
رکوردی نباید به engine برسد. `salamander` بدون رمز رفتار پیشین خود را نگه می‌دارد
(`invalid` با `invalid_obfs`) و نوع obfs ناشناخته هم همان `invalid_obfs` است. همین
دروازه برای outbound JSON هیستریا۲ (بخش ۵.۸) هم اعمال می‌شود. اگر رکورد gecko بدون
رمز پیش از این دروازه در state ذخیره شده باشد، نمای pool آن را
`unsupported`/`missing_obfs_password` مشتق می‌کند (بخش ۵.۳)، probe و dial نمی‌شود،
از کانفیگ sidecar کنار گذاشته می‌شود و exitهای سالم hysteria2 کار خود را ادامه
می‌دهند؛ state بازنویسی نمی‌شود.

### ۵.۵ TLS ناامن (D18) و pin

هیچ واحد/فرمان/فیلد رضایتی در v1 وجود ندارد. دلیل: هستهٔ pin‌شده `allowInsecure` را
حذف کرده و هیچ راهی برای خاموش‌کردن verification در outbound VLESS/Trojan نیست؛
پذیرش کاربر نمی‌تواند رکورد را قابل dial کند، پس prompt و state مرده است.

- رکورد skip-verify در همان import `unsupported` می‌شود؛ برچسب قرنطینه به‌صورت پرچم
  `insecure_tls` و دلیل پایدار `insecure_tls_removed` باقی می‌ماند، با hint: «لینک را با
  گواهی معتبر یا `pinnedPeerCertSha256` جایگزین کن». دلیل و پرچم در `pool list`،
  `status` و `doctor` دیده می‌شود؛ اتصالی ساخته نمی‌شود و هیچ تنظیم امنیتی تغییر نمی‌کند.
- `pinnedPeerCertSha256` و `verifyPeerCertByName` (هر دو در هستهٔ pin‌شده پشتیبانی
  می‌شوند) پذیرفته می‌شوند، فرمتشان validate می‌شود و verification روشن می‌ماند. این **pin است، نه استثنای ناامن**: فقط همان
  گواهی/نام پذیرفته می‌شود و هیچ رکورد دیگری را تحت تأثیر قرار نمی‌دهد.
- بازگرداندن مسیر رضایت فقط زمانی مجاز است که یک استثنای محدود، قابل‌مشاهده و
  قابل‌لغو واقعاً قابل پیاده‌سازی باشد؛ آن زمان نیازمند قرارداد جدید است (R-14).

### ۵.۶ محدودیت fetch اشتراک

| محدودیت | مقدار |
| --- | --- |
| اندازهٔ بدنه | ۴ MiB |
| اتصال / کل | ۱۰s / ۳۰s |
| redirect | حداکثر ۳، فقط http/https؛ تغییر host → حذف Authorization/Cookie |
| پروکسی محیط | هرگز (`Proxy: nil`)؛ TLS verification روشن |
| رکورد در هر منبع | ۲۰۰۰ |
| non-2xx | `source_fetch`؛ pool قبلی دست‌نخورده |

پارس: بدنهٔ base64 (standard/URL-safe، با/بدون padding) یا فهرست لینک خط‌به‌خط؛ اگر
اولین بایت غیرفاصله `<` باشد (HTML) → خطا، نه «منبع خالی». هیچ محتوایی اجرا یا fetch
نمی‌شود؛ فقط رشته‌ها parse می‌شوند. تعداد خطای هر رکورد در خروجی گزارش می‌شود.

### ۵.۷ refresh اتمیک و آخرین دادهٔ سالم

یک `Store.Mutate` برای کل refresh: ساخت مجموعهٔ جدید از منبع(ها) → validate/sanitize →
merge بر اساس `DedupeKey` → commit. اگر یک منبع شکست بخورد، snapshot قبلی همان منبع
دست‌نخورده می‌ماند و خروجی `ok=false` با دلیل می‌دهد. صفر رکورد معتبر بدون
`--allow-empty` خطاست، نه پاک‌کردن pool. pool سالم قبلی هرگز به‌خاطر HTML/خطای شبکه
از دست نمی‌رود.

### ۵.۸ v2rayN

- **مرجع schema:** `2dust/v2rayN` tag `7.9.3` (commit `064a04f`)،
  `v2rayN/ServiceLib/Models/ProfileItem.cs` و `Enums/EConfigType.cs`
  (`VMess=1, Custom=2, Shadowsocks=3, SOCKS=4, VLESS=5, Trojan=6, Hysteria2=7,
  TUIC=8, WireGuard=9, HTTP=10`). پروفایل‌ها در `guiNDB.db` (SQLite) و تنظیمات اپ در
  `guiNConfig.json` هستند.
- **v1 می‌پذیرد:** فایل متنی لینک‌های share (یک در هر خط، یا blob base64) و خروجی
  export خود v2rayN. VLESS/Trojan/SOCKS5 نگاشت می‌شوند؛ HTTP/REALITY و بقیهٔ انواع
  `unsupported` می‌شوند (`reality` خارج از v1 است).
- **v1 نمی‌پذیرد:** `guiNDB.db` (نیازمند driver جدید) و `guiNConfig.json` (تنظیمات اپ:
  inbound/routing/TUN و ...). پیام: «لینک‌ها را export کن»؛ exit `7`. هیچ driver یا
  خواندن SQLite اضافه نمی‌شود.
- **فقط‌خواندنی:** importer هرگز فایل v2rayN را نمی‌نویسد، قفل نمی‌کند و پوشهٔ آن را
  تغییر نمی‌دهد؛ فقط مسیری که کاربر می‌دهد خوانده می‌شود.

### ۵.۹ export و backup

`pool export` به‌طور پیش‌فرض رکوردهای **redacted** (بدون credential و بدون URL منبع)
می‌دهد. بازسازی share link فقط با `--with-secrets --out <file>` صریح، فایل `0600`،
هشدار روی stderr و بدون چاپ در stdout انجام می‌شود.

## ۶. probe و سلامت

### ۶.۱ نوع‌ها و triggerها

| نوع | چه می‌سنجد | triggerهای مجاز |
| --- | --- | --- |
| `transport` | فقط endpoint خروجی: TCP و در صورت نیاز TLS/pin | startup، rotation، rerank، manual، after-failure |
| `egress` | dial از exit به هدف پیکربندی‌شده + اولین بایت | manual، rerank، rotation (اگر هدف تعریف شده) |
| `direct_tcp` | اتصال مستقیم به مقصد، بدون بایت برنامه | failure (تشخیص)، manual |
| `direct_tls` | TLS مستقیم با SNI و verification کامل | failure (تشخیص)، manual |

نوع probe به نوع مقصد وابسته است: hostname → `direct_tcp` و در صورت شکست TLS یا پورت
۴۴۳ → `direct_tls` با SNI همان نام و verification کامل؛ IP بدون نام → فقط
`direct_tcp` و نتیجه با برچسب «فقط همان مرحله» ثبت می‌شود (هیچ verification خاموش
نمی‌شود). exit خروجی همیشه با `transport` و در صورت لزوم pin بررسی می‌شود.
probe از همان `core.Instance` فعال با tag صریح استفاده می‌کند؛ ساختن core دوم در همان
process به‌خاطر وضعیت سراسری مجاز نیست.

`Probes.Targets` پیش‌فرض خالی است: RouteLatch هیچ مقصد ثالثِ اختراعی برای «تست اینترنت»
ندارد. بدون هدف، سلامت از سیگنال transport + شواهد مثبت استفادهٔ واقعی ساخته می‌شود؛
`pool test --target` یا تنظیم `probes.targets` یک انتخاب صریح کاربر است.

### ۶.۲ انتساب شواهد

- **مثبت:** بایت برگشتی از تونل (downlink > 0) یعنی مسیر کار کرد → نمونهٔ موفق و EWMA.
- **منفی قابل انتساب:** `exit_unreachable`، `handshake_rejected` و `timeout` فاز dial.
  این‌ها cooling می‌سازند.
- **نمونهٔ pre-byte یک CONNECT:** اگر exit پس از proof و پیش از اولین بایت مقصد تونل را
  ببندد، یک نمونهٔ شکست برای همان exit ثبت می‌شود (kind `destination_unconfirmed`) و
  cooling می‌سازد. این «exit نتوانست تونل را تا بایت اول نگه دارد» است، نه «مقصد
  refused/در دسترس است»؛ محدودیت ثبت‌شده: مقصدی که خودش پیش از بایت اول می‌بندد هم
  همین اثر را دارد و می‌تواند exitهای تلاش‌شده را cooling کند.
- `destination_unconfirmed` در probe/stream معمولی (بدون پنجرهٔ CONNECT) نه cooling
  می‌سازد و نه موفقیت؛ در آمار «بدون شاهد» ثبت می‌شود (T1: علت مقصد در VLESS/Trojan
  قابل تفکیک نیست). پیامد صریح: UUID/password غلط یا خروجی کاملاً مرده هم در همین دسته
  می‌ماند و ممکن است تا اولین شاهد مثبت «healthy» نشود ولی cooling هم نشود؛
  `pool test --target` ابزار کاربر برای جداکردن این حالت است.
- انتساب در `ProbeResult.Attribution` حمل می‌شود: فقط `exit` اجازهٔ cooling می‌دهد،
  `local_outage` هرگز exit را مقصر نمی‌کند و `none` یعنی هیچ شاهد سطح exit وجود ندارد
  (probe مستقیم مقصد، stream تأییدنشده، توقف پس از dial). این فیلد جای پارامتر خارج از
  قرارداد را گرفته و تنها دروازهٔ انتساب است؛ تصمیم سلامت همچنان outcome و failure kind
  را هم می‌بیند.
- probe مستقیم فقط برای تشخیص است و هرگز مبنای انتخاب exit نیست.

### ۶.۳ رفتار در حالت‌ها

| حالت | probe خودکار | دستی |
| --- | --- | --- |
| هدفمند / همه از پروکسی | transport (startup + هر ۱۰ دقیقه + after-failure)؛ egress فقط با trigger صریح/rerank | مجاز |
| همه مستقیم | هیچ probe خودکار (نه pool، نه تشخیص)؛ health با تاریخ کهنه نمایش داده می‌شود | `pool test` مجاز با اخطار «ترافیک از pool می‌رود، نه از حالت»؛ `diagnose --direct` مجاز |
| متوقف | probe و worker وجود ندارد؛ listenerها بسته‌اند | management socket پاسخ می‌دهد: `status`/`rules list`/`pool list` exit `0` با `mode=stopped`؛ `env`/`exec` و `pool test` → exit `3`؛ خروج از stopped فقط با process جدید |

خاموش‌کردن `diagnostics.enabled` صف را لغو می‌کند (`CancelQueued`)، پیشنهاد جدید نمی‌سازد،
probe مقصد جدید را متوقف می‌کند؛ health pool و فرمان دستی مستقل می‌مانند (D09).

### ۶.۴ اعداد انتخاب

| پارامتر | مقدار | منطق |
| --- | --- | --- |
| cooldown پایه | ۶۰s | اولین شکست transport |
| رشد | ×۲ تا سقف ۱۵ دقیقه | پس از ۴ شکست متوالی به سقف |
| بازنشانی | موفقیت | شمارنده صفر |
| EWMA latency | α = ۰٫۳ | نمونهٔ جدید وزن بیشتر |
| شاهد استفادهٔ واقعی | حداکثر ۱ نمونه در ثانیه برای هر exit | اولین بایت برگشتی از تونل واقعی؛ coalescing تا هر اتصال یک fsync نسازد |
| حد سوییچ | بهبود ≥ ۲۰٪ و ≥ ۲ نمونهٔ موفق | D16: ضد flapping |
| حد اقامت | ۵ دقیقه | بین دو سوییچ |
| deadline probe | ۲۰s کل، ۸s انتظار اولین بایت | dial ≤۱۰s + first byte ≤۸s |
| سقف `pool test` | ۲۰ exit در هر فراخوانی | deadline سوکت متناسب با تعداد؛ partial مجاز |
| فاصلهٔ transport probe خودکار | ۱۰ دقیقه | فقط در targeted/all-proxy؛ بدون ترافیک مقصد |
| هم‌زمانی probe | health ۲، diagnostic ۱، صف ۳۲ | سقف منابع |
| نمونه/نگهداری | ۲۰۰ نمونه، ۷ روز (ring حافظه‌ای) | — |
| پیشنهاد | ۳۰ روز (TTL حافظه؛ persist نمی‌شود) | — |

**نگهداری نمونه.** تاریخ نمونه‌ها (۲۰۰ نمونه و ۷ روز برای هر exit) یک ring حافظه‌ای
محدود در prober است و هرگز در `state.json` نوشته نمی‌شود. آنچه persist می‌شود خلاصهٔ
`ExitHealth` است (وضعیت، EWMA، شمارنده‌ها، cooldown و آخرین kind)؛ پس از restart همان
خلاصه بارگذاری می‌شود و نمونه‌های خام از دست می‌روند. سلامت پس از restart از probeهای
transport و شواهد واقعی egress دوباره یاد گرفته می‌شود و هیچ healthyای از ring قدیمی
ادعا نمی‌شود. `HealthSamplesPerExit` و `HealthRetention` همین ring را می‌بندند، نه فایل
state. پیشنهادها و شواهدشان هم فقط در حافظه‌اند: TTL ۳۰ روزه یک سقف حافظه برای سرویس
زنده است و restart هیچ پیشنهاد یا شاهدی را بازنمی‌گرداند (D21).

رتبه: `healthy` پیش از `unknown`؛ درون هر bucket، EWMA صعودی، سپس ترتیب کاربر. انتخاب
دستی تا `pool select auto` پین می‌ماند و با شکست به exit دیگری سوییچ نمی‌کند.

منشأ اعداد: deadlineهای dial/probe و آستانه‌های teardown از اندازه‌گیری T1
می‌آیند. آستانه‌های رتبه‌بندی/cooldown تصمیم اولیهٔ مهندسی‌اند (T1 توزیع latency چند
خروجی را نسنجید)؛ T4 باید آن‌ها را با دادهٔ مجاز کالیبره کند و تغییرشان قرارداد جدید
است، نه تنظیم پنهان.

## ۷. سطح CLI و منو

### ۷.۱ فرمان‌ها، JSON و exit

همهٔ فرمان‌ها `--json` دارند و `Envelope` (`internal/contract/cli.go`) را با
`schema_version: 1` چاپ می‌کنند. خروجی انسانی هم به stdout می‌رود؛ هشدارها به stderr.
`run` پیش‌فرض اپ است: از ترمینال detach می‌شود، tray/GUI (طبق D26) را بالا می‌آورد و
فقط بعد از readiness برمی‌گردد. `run --headless` همان engine foreground است و تا
SIGINT/SIGTERM بلاک می‌ماند؛ همان چیزی است که unit opt-in و تست‌ها اجرا می‌کنند.
هیچ‌کدام listener جایگزین یا proxy دسکتاپ خودکار نمی‌سازند. **وضعیت:** D26 در R13
پیاده‌سازی می‌شود؛ تا آن زمان `run` فعلی foreground است و unit موجود رفتار قبلی را دارد.

`rlch` یک نام کوتاه نصب‌شده است (symlink به `routelatch`)، نه فرمان تازه؛
`argv[0]` نام فراخوانی‌شده را نشان می‌دهد، ولی گزینه‌ها/آرگومان‌ها، stdout/stderr،
JSON، exit code و معنای همهٔ فرمان‌های جدول یکی است. `routelatch` نام قراردادی و
مسیر `ExecStart` unit opt-in (`run --headless`) می‌ماند؛ اسکریپت‌ها و سرویس‌ها از
نام اصلی استفاده می‌کنند.

```json
{ "schema_version": 1, "ok": true, "command": "status", "data": { "...": "..." } }
{ "schema_version": 1, "ok": false, "command": "pool refresh",
  "error": { "kind": "source_fetch", "path": "local", "message": "subscription returned HTTP 503", "detail": { "source_id": "s-..." } } }
```

`data` دقیقاً همان کلیدهای ستون دوم است؛ دو نمونهٔ کامل:

```json
{ "mode": "targeted", "ready": true, "listeners_open": true,
  "resume_requires_restart": false, "policy_version": 7, "selected_exit": "",
  "listeners": { "http": "127.0.0.1:8317", "socks": "127.0.0.1:8318" },
  "pool": { "total": 12, "usable": 9, "healthy": 6, "cooling": 2 },
  "connections": { "open": 3, "dialing": 1, "proofs": { "confirmed": 0, "accepted": 2 } },
  "diagnostics": { "enabled": true, "queued": 0 },
  "tun": { "desired": false, "state": "off", "interface": "", "dns": "direct",
           "udp": "blocked", "ipv6": false, "helper": "missing" },
  "warnings": ["1 record is unsupported: insecure_tls_removed (replace the link or use pinnedPeerCertSha256)"] }

{ "mode": "all-proxy", "policy_version": 8,
  "closed": { "count": 4, "pending": 1 }, "kept": 2, "duration_ms": 12 }
```

> فرمان‌های `tun status|on|off|setup|uninstall` در R16-E اضافه شدند؛ معنای opها،
> خطاها و exitها در §۱۰.۳ و قرارداد helper/unit در §۱۰.۴ است. `setup|uninstall`
> privileged و فقط CLI هستند و از management socket عبور نمی‌کنند.
>
> شیء `tun` در `data` یک افزودهٔ schema 1 است: payload قدیمی آن را ندارد و
> مصرف‌کننده آن را nil/unknown می‌بیند. `since`/`last_error`/`detail` وقتی مقدار
> ندارند حذف می‌شوند؛ شکل کامل و معنای `helper=missing` در §۱۰.۲ است.

| فرمان | `data` (خلاصه) | exitهای ممکن | معادل منو |
| --- | --- | --- | --- |
| `run [--headless]` | پیش‌فرض: `pid, listeners, log, already_running?` یا `state/reason`؛ `--headless`: `listeners` در shutdown | 0، 1، 2، 4، 6، 7، 8، 9، 10، 12 | «شروع اپ (tray)» / «شروع headless» |
| `autostart enable\|disable\|status` | `path, enabled, managed, tray_available, pidfd_available, conflict?` | 0، 1، 2، 6، 7 | — |
| `service install\|uninstall\|status` | `unit, enabled, active, linger, managed, conflict?` | 0، 1، 2، 6، 7، 8، 9، 10، 12 | — |
| `version` | `version, commit, build_date` | 0 | — |
| `status` | `mode, ready, listeners_open, resume_requires_restart (R15), policy_version, selected_exit, listeners, pool{total,usable,healthy,cooling}, connections{open,dialing,proofs{confirmed,accepted}}, diagnostics, tun{desired,state,interface,since?,dns,udp,ipv6,helper,last_error?,detail?}, warnings` | 0، 3، 10 | «وضعیت» |
| `mode <mode>` | `mode, policy_version, closed{count,pending}, kept` | 0، 3، 6، 7 | «تغییر حالت» |
| `tun status` | `tun{desired,state,interface,since?,dns,udp,ipv6,helper,last_error?,detail?}` | 0، 1، 3 | «TUN coverage» |
| `tun on` | همان `tun` | 0، 1، 3، 6، 7 | «TUN coverage» |
| `tun off` | همان `tun` | 0، 1، 3 | «TUN coverage» |
| `tun setup\|uninstall` | `unit, socket_unit, config, binary, allowed_uid, changed, managed, removed?` | 0، 1، 2، 6، 7، 9 | — |
| `rules list` | `rules[{id,kind,host,addr,prefix,note,origin,added_at}]` | 0، 3 | «فهرست مقصدها» |
| `rules add <input>` | `rule{...}, normalized, notice?` | 0، 2، 3، 6، 7 | «افزودن مقصد» |
| `rules remove <id>` | `removed, closed` | 0، 3، 5، 6 | «حذف مقصد» |
| `pool list [--limit] [--offset]` | `records[{id,status,status_reason,protocol,server,port,transport,security,sources,health{...}}], counts, truncated` | 0، 3 | «pool» |
| `pool add <link\|file>` | `source_id, records{valid,invalid,unsupported}` | 0، 2، 6 | «افزودن خروجی» |
| `pool remove <id>` | `removed, closed` | 0، 3، 5، 6 | «حذف خروجی» |
| `pool refresh [--source\|--all] [--allow-empty]` | `sources[{id,ok,records,error}], kept_last_good` | 0، 3، 5، 6، 8 | «به‌روزرسانی pool» |
| `pool test [--exit\|--target]` | `results[{exit,kind,outcome,duration_ms,bytes_down}]` | 0، 3، 8، 11 | «آزمون pool» |
| `pool select <id\|auto>` | `selected, policy_version` | 0، 3، 5، 6 | «انتخاب خروجی» |
| `pool rerank` | `selected_before, selected_after, skipped[{exit,reason: pinned\|cooling\|unsupported\|invalid\|unknown\|dwell\|samples\|improvement\|no_target}], policy_version` | 0، 3، 11 | «رتبه‌بندی دوباره» |
| `import link\|file\|subscription\|v2rayn` | `source_id, records{valid,invalid,unsupported}` | 0، 2، 6، 7، 8 | «واردکردن» |
| `diagnose <host:port> [--direct\|--pool]` | `destination, kind, attempts[{outcome,duration_ms}], eligible, proposal_id?` | 0، 2، 3، 8، 11 | «تشخیص» |
| `proposals list\|accept\|reject\|recheck\|clear` | `proposals[{id,kind,rule_id,destination,status,expires_at}]` / `rule_id` / `cleared` | 0، 3، 5، 6 | «پیشنهادها» |
| `config get\|set\|path` | `path, values` | 0، 2، 3، 6، 7 | «تنظیمات» |
| `doctor` | `checks[{name,status,detail,fix}]` | 0، 3، 10 | «بررسی سلامت» |
| `env` / `exec -- <cmd>` | `HTTP_PROXY,HTTPS_PROXY,ALL_PROXY,NO_PROXY` / child exit code | 0، 3، 7 + کد فرزند | «اجرای برنامه» |
| `desktop proxy status\|set\|restore` | `environment, supported, applied, previous_set, conflict` | 0، 1، 3، 6، 7، 8، 10، 12 | «پروکسی دسکتاپ» |
| `backup` / `restore <ref>` | `ref{path,schema_version,created_at}` | 0، 3، 6، 10 | «پشتیبان» |
| `menu` | — | 0، 2، 3 | خودش |

`pool remove <id>` تنها مسیر حذف رکورد است (از جمله رکورد `unsupported` با دلیل
`engine_record_rejected`)؛ v1 هیچ فرمان حذف آفلاین ندارد.

**`ready` و `listeners_open`.** management socket پیش از `StartNetwork` هم پاسخ
می‌دهد، بنابراین `status` به‌تنهایی آمادگی نیست. `ready=true` فقط وقتی است که
management در حال سرویس‌دهی و — اگر mode ذخیره‌شده `stopped` نیست — هر دو listener
لازم bind/پذیرش‌شده باشند؛ در `stopped` معتبر، `ready=true` و `listeners_open=false`
است. `listeners_open` فقط بازبودن listenerهای لازم همان mode را نشان می‌دهد.
`ready=false` وضعیت startup/transition است، نه خطا؛ `service install`، supervisor
اپ و بازبینی `already_running` همه باید همین فیلد را (به‌جای `ActiveState` یا
وجود listener address) ملاک بگیرند. این دو فیلد additive هستند و schema_version
را تغییر نمی‌دهند.

**`resume_requires_restart` (R15؛ پیاده‌شده).** این فیلد additive
فقط وقتی `true` است که همین process وارد `stopped` شده باشد؛ در process تازه‌ای
که با state ذخیره‌شدهٔ `stopped` بالا آمده `false` است و تغییر mode مستقیماً
listenerها را باز می‌کند. tray با همین فیلد، actionهای mode را در حالت
stopped-by-process غیرفعال و پس از restart فعال می‌کند؛ این وضعیت با restart
خود tray از بین نمی‌رود. هشدار `status` هم باید همین تفاوت را منعکس کند، نه
پیام ثابت «restart لازم است»: در stopped-by-process می‌گوید ترک این mode
restart لازم دارد و در process تازه می‌گوید با تغییر mode ادامه دهد و restart
لازم نیست. شکل encode صریح است و `status` همیشه این کلید را با `true`/`false`
می‌فرستد (در modeهای غیر stopped مقدار `false` دارد)؛ decode سه‌حالته است و
نبود کلید در payload قدیمی «نامعلوم» می‌ماند (در Go با `*bool` و nil)، نه
`false`. schema_version تغییر نمی‌کند؛ در app mode
engine/tray هم‌باینری‌اند، و اگر tray با سرویس قدیمی‌تری که فیلد را ندارد حرف
بزند آن را «نامعلوم» می‌گیرد و در stopped فقط پس از یک restart موفق mode را
فعال می‌کند.

شمارندهٔ unsupported در هشدارهای `status` و در checkهای `doctor` از همان نمای مشتق‌شدهٔ
`pool list` می‌آید: رکورد قدیمی با پروتکل غیرقابل‌dial در هر status ذخیره‌شده به‌جز
`invalid` در هر سه سطح `unsupported` شمرده می‌شود و در شمار `unknown`/`cooling`
نمی‌ماند (§۵.۳).

`exec` در حالت `--json` دقیقاً یک `Envelope` روی stdout می‌نویسد و stdout و stderr
فرزند را به stderr هدایت می‌کند؛ exit code فرمان همان exit code فرزند است. در حالت
انسانی، جریان‌های فرزند روی descriptorهای اصلی خودشان می‌مانند.

`status` در v1 هیچ شمارندهٔ بایتی برای اتصال‌ها ندارد و `ConnInfo` فیلد byte ندارد؛ برای
اندازه/اثر یک تغییر mode یا rule از `CloseReport` استفاده کن (فیلدهای `Closed`،
`PendingCancel`، `Kept`، `Duration`)، نه از تخمین ترافیک.

### ۷.۲ exit codeها

`0` موفق، `1` خطای عمومی، `2` usage/ورودی نامعتبر، `3` سرویس در دسترس نیست،
`4` نمونهٔ دیگر فعال است، `5` شناسه پیدا نشد، `6` تعارض/تغییر رد شد (lock، version،
restore ناامن)، `7` پلتفرم/ویژگی خارج v1، `8` timeout، `9` peer/مجوز رد شد،
`10` state خراب/نسخهٔ جدیدتر، `11` pool در دسترس نیست، `12` لغو شد. نگاشت kind→exit
دقیقاً `contract.ExitCodeFor` است و تست هر ۲۲ kind و بسته‌بودن مجموعه را پوشش می‌دهد.
`not_found` → `5` (lookup فرمان) و `external_tool_failed` → `1` (شکست
`gsettings`/`kwriteconfig`/`systemctl`/`loginctl`؛ نتیجهٔ تنظیم نامعلوم اعلام
می‌شود). نبود tray با `unsupported`/`7` و دلیل `tray_unavailable` گزارش می‌شود؛
`run` پیش‌فرض وقتی اپ از قبل در حال اجراست exit `0` با `already_running=true` می‌دهد،
ولی engine `--headless` دوم و unit هم‌زمان همچنان exit `4` می‌گیرند. refuse کردن
`service install` برای کاربر دیگر با `policy_denied`/`9` است.

### ۷.۳ منو و tray control surface

منوی huh فقط پوستهٔ همین فرمان‌هاست: هر عمل یک فرمان با همان JSON و همان exit code را
صدا می‌زند. عمل مخرب (حذف قانون/pool، restore، توقف) تأیید صریح می‌خواهد و
پیش‌فرض «نه» است. منو هیچ منطق مسیری جدا ندارد. پس از R13 منو باید دو ورودی
جدا داشته باشد: «Start app (tray)» → `run` و «Start headless (foreground)» →
`run --headless`؛ نگاشت قدیمی «Start the service» → `run` کافی نیست (A10/A15).

**سطح کنترل tray (D28، ۲۰۲۶-۱۱-۲۵؛ controller/model در R15-B و اتصال واقعی deedles در R15-D2 پیاده شدند).** tray child یک
کلاینت نازک روی همان management socket است و هیچ منطق مسیریابی، pool، rule،
policy یا state ندارد:

- socket از `TrayOptions.Socket` و data-dir از `TrayOptions.DataDir` می‌آید که
  `cmd/routelatch` آن‌ها را از `store.Paths` پر می‌کند؛ اتصال همان peer check
  same-uid و محدودیت ۶۰ KiB/۴ MiB را دارد. tray هیچ فایل state/config را
  مستقیم نمی‌خواند یا نمی‌نویسد و listener جدید باز نمی‌کند.
- `quit`، `restart-engine`، `lost` و `error <reason>` تنها پیام‌های
  tray→supervisor هستند؛ عملیات مدیریتی از socket می‌روند. supervisor فقط
  رویدادهای lifecycle (`restarting`، `ready`) را برمی‌گرداند (§۷.۵). هیچ op
  جدیدی برای stop/restart supervisor اضافه نمی‌شود.
- handler هر آیتم D-Bus فقط action را ثبت می‌کند و برمی‌گردد؛ opهای طولانی
  asynchronous اجرا می‌شوند (بند jobها). handler هرگز منتظر dial/probe/refresh
  نمی‌ماند. ثبت tray و پیام `ready` به supervisor به load دادهٔ منو گره
  نمی‌خورد؛ تا snapshot اول یک منوی حداقلی «Loading…» با actionهای غیرفعال
  نمایش داده می‌شود و app-readiness همچنان فقط به `status.ready` engine و
  ثبت tray وابسته است.
- زبان UI در این milestone **English** است (D28)؛ i18n و ترجمه خارج از دامنه‌اند.
- GUI کامل خارج از دامنه است؛ یک GUI آینده باید از همان management contract
  استفاده کند، نه از `internal/trayui`.

**درخت منو (قرارداد).** ترتیب آیتم‌های اصلی در هر حالت ثابت است؛ آیتم‌های
وابسته به state (mode/stopped، desktop، Quit) با `enabled=false` یا
`visible=false` کنترل می‌شوند و حذف بی‌صدا نمی‌شوند. متن داخل `<>` پویا و
sanitize شده است. اگر host رادیو/checkmark را رندر نکند (نتیجهٔ R15-A)، وضعیت
انتخاب با `✓` یا `(current)` در label نشان داده می‌شود؛ action و داده یکی
می‌مانند.

```text
RouteLatch
  <status line>                                  disabled
  Mode ▸
    Targeted | All proxy | All direct            radio
    Stop listening…                              pending confirmation
  Proposals (N) ▸
    <pending proposal> ▸
      Accept | Reject | Recheck
    Show decided (N) ▸
    Clear decided/expired…                       pending confirmation
  Approved rules (N) ▸
    <rule> ▸
      Remove…                                    pending confirmation
      Details                                    disabled
    Page x/y · Previous page · Next page
    Add rule… (Terminal menu)
  Pool ▸
    <summary>                                    disabled
    Auto select (radio) | <pinned exit>
    Healthy (N) ▸ pages
    Untested (N) ▸ pages
    Cooling (N) ▸ pages
    Unsupported/Invalid (N) ▸ pages
    Refresh all sources                          job
    Re-rank now
    Test selected exit | Test first 20 usable    job
    Add/import… (Terminal menu)
  Desktop proxy ▸
    <status>                                     disabled
    Refresh status
    Set/Re-apply desktop proxy…                  pending confirmation
    Restore previous desktop proxy…              pending confirmation
  Diagnostics ▸
    Run doctor                                   job
    Doctor results ▸
    Warnings (N) ▸
    Show log path
    Environment ▸
  Data ▸
    Back up state now                            job
    Restore backup… (CLI)                        disabled hint
  Settings ▸
    Start at login: On/Off                       checkmark
    <config summary>                             disabled
    <data paths>                                 disabled
  Advanced ▸
    Restart engine…                              pending confirmation
    Terminal menu…
  Last action: <short result>                    disabled
  Quit ▸
    Quit RouteLatch
    Restore desktop proxy and quit…              pending confirmation
    Quit and keep desktop proxy…                 pending confirmation
```

هدر وضعیت کوتاه است: `Targeted · 156 usable · 3 open`،
`All direct · 3 open`، `Stopped · listeners closed` یا
`Engine unavailable; retrying…`. در all-direct شمار pool در هدر نمایش داده
نمی‌شود چون انتخاب خروجی موضوعیت ندارد؛ در stopped/unavailable actionهای
نامعتبر غیرفعال می‌شوند. `Restart engine…` در حالت stopped به‌صورت یک آیتم اضافی در ریشه با
متن «Restart engine to resume…» هم دیده می‌شود؛ action یکی است. صفحه‌بندی pool و
rules ۲۰–۲۵ آیتم دارد (تعداد دقیق در پیاده‌سازی ثابت و تست می‌شود) و گروه‌های
pool از `Counts` و `status` همان `pool list` می‌آیند. «Test first 20 usable»
همان سقف `MaxPoolTestExits` قرارداد است؛ tray نباید ادعای تست کل pool بدهد.
`N` در «Proposals (N)» تعداد pending است؛ decided/expired زیر
«Show decided» و در صورت زیادبودن با همان صفحه‌بندی ۲۰–۲۵تایی می‌آیند. اگر
`status.diagnostics.enabled=false` باشد، Accept/Reject/Recheck غیرفعال‌اند
(engine پیشنهاد جدیدی نمی‌سازد و این opها به diag نیاز دارند)؛ نمایش و
Clear decided/expired باز می‌مانند. `Details` یک rule شامل ID کوتاه، kind، operand، note
sanitize‌شده و added_at است. زیرمنوهای خالی یک آیتم disabled با متن
`No …` و در صورت لزوم اشاره به `Terminal menu` دارند، نه یک منوی خالی. `Doctor results` هر check را به‌صورت `[ok|warn|error] name: detail` کوتاه
نشان می‌دهد؛ `Warnings` از `status` می‌آید؛ `Environment` چهار مقدار
`OpEnv` (`HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY`/`NO_PROXY`) را نمایش می‌دهد؛
`config summary` از `config get` فقط `http_listen`/`socks_listen`/`log_level`
را می‌آورد و `data paths` از `config path` می‌آید. `Show log path` فقط مسیر
sanitize‌شده را notification می‌کند. `Start at login` با data-dir سفارشی یا
`conflict=service` غیرفعال و با دلیل نمایش داده می‌شود؛ تغییر آن فقط از طریق
همان `autostart.Manager` و با اقدام صریح کاربر است. `Desktop proxy` فقط وقتی
`supported=true` است actionهای Set/Restore/Refresh را فعال می‌کند؛ در محیط
پشتیبانی‌نشده فقط status و دلیل نمایش داده می‌شود.

**نگاشت actionها.**

| آیتم | request/مسیر | تأیید | job |
| --- | --- | --- | --- |
| Mode: targeted/all-proxy/all-direct | `mode` با همان مقدار | نه | نه |
| Stop listening | `mode=stopped` | بله | نه |
| Restart engine | pipe `restart-engine` به supervisor | بله | بله (انتظار readiness) |
| Proposal Accept (route) | `proposals.accept` | نه (کلیک همان تأیید است) | نه |
| Proposal Accept (unroute) | `proposals.accept` | بله (حذف rule) | نه |
| Proposal Reject | `proposals.reject` | نه | نه |
| Proposal Recheck | `proposals.recheck` | نه | بله |
| Clear decided/expired | `proposals.clear` | بله | نه |
| Rule Remove | `rules.remove` | بله | نه |
| Pool Auto/Pin | `pool.select` با `auto` یا id | نه | نه |
| Pool Remove | `pool.remove` | بله | نه |
| Pool Refresh all | `pool.refresh` با `all=true` | نه | بله |
| Pool Test selected / first 20 | `pool.test` با id یا بدون id | نه | بله |
| Pool Re-rank | `pool.rerank` | نه | نه |
| Desktop Refresh status | `desktop.status` | نه | نه |
| Desktop Set/Re-apply | `desktop.set` | بله | بله |
| Desktop Restore | `desktop.restore` | بله | بله |
| Back up now | `backup` | نه | بله |
| Run doctor | `doctor` | نه | بله |
| Start at login | `autostart.Manager.Status/Enable/Disable` محلی | نه | بله (محلی) |
| Terminal menu | اجرای همان باینری با `menu` | نه | نه |
| Quit RouteLatch | pipe `quit` | فقط اگر desktop applied یا job در حال اجرا باشد | نه |
| Restore desktop proxy and quit | `desktop.restore` سپس pipe `quit` | بله | بله |
| Quit and keep desktop proxy | pipe `quit` | بله اگر desktop applied باشد | نه |

`add/import` pool، `rules add`، `diagnose`، `config set`، `restore <path>` و
`exec` در tray نیستند و از `Terminal menu…` یا CLI انجام می‌شوند؛
`Rules → Add rule…` و `Pool → Add/import…` فقط همان action ترمینال را باز
می‌کنند (متن در ترمینال وارد می‌شود، نه در tray). مدیریت
`service install/uninstall/status` هم در tray نیست؛ این یک اقدام نصب/میزبان است
و با app/tray هم‌زمان تعارض دارد. برای unroute proposal، `Recheck` غیرفعال است
چون شواهد probe ندارد؛ Accept آن چون rule را حذف می‌کند pending می‌خواهد.

**تأیید دو مرحله‌ای.** action مخرب با کلیک اول اجرا نمی‌شود: یک pending در
حافظهٔ tray ساخته می‌شود و در ریشهٔ منو دو آیتم «Confirm: <label>» و «Cancel»
ظاهر می‌شوند. pending پس از ۶۰ ثانیه منقضی می‌شود، فقط یکی هم‌زمان وجود دارد،
با action جدید جایگزین یا لغو می‌شود، و هرگز در state/config/log نوشته نمی‌شود.
Confirm دقیقاً همان action ذخیره‌شده را اجرا می‌کند؛ label هرگز برای استخراج
id/action پارس نمی‌شود. فهرست حداقلی actionهای pending در جدول بالا آمده است.
عمل «Accept» برای proposal route با کلیک خود کاربر تأیید می‌شود؛ چون این کلیک
یک افزودن صریح به فهرست approved است و نیازی به pending دوم ندارد.

**jobها و refresh.** حداکثر یک job طولانی هم‌زمان اجرا می‌شود؛ تا پایان آن،
actionهای تغییردهنده disabled‌اند و ریشه یک آیتم «Job: … running» نشان می‌دهد.
jobهای تعریف‌شده: pool refresh/test، proposal recheck، desktop set/restore،
backup، doctor، restart و autostart toggle. status هر ۲ ثانیه و لیست‌ها هر ۱۵
ثانیه (و بلافاصله پس از action) refresh می‌شوند؛ فراخوانی status/list یک
deadline کوتاه (۱–۲ ثانیه) دارد، در goroutine جدا اجرا می‌شود و tick بعدی را
بلوکه نمی‌کند؛ تغییرها با fingerprint مقایسه می‌شوند تا rebuild بی‌مورد رخ
ندهد. desktop/autostart به‌صورت دوره‌ای گران
هستند و فقط در شروع، پس از action و هنگام Quit تازه می‌شوند. handler هرگز
منتظر این فراخوانی‌ها نمی‌ماند. اگر socket در دسترس نباشد یا status/list با
deadline پاسخ ندهد، منو «Engine unavailable; retrying…» نشان می‌دهد،
لیست‌های پویا را پاک می‌کند و actionهای مدیریتی را غیرفعال نگه می‌دارد تا
بازیابی؛ دادهٔ کهنه به‌عنوان وضعیت جاری نمایش داده نمی‌شود. در restart، آیتم
«Restarting engine…» و poll سریع‌تر (۵۰۰ms) تا بازگشت readiness استفاده می‌شود.
اگر job به‌خاطر از دست رفتن engine قطع شود، پیام روشن «interrupted» نمایش داده
می‌شود؛ tray ادعا نمی‌کند عملیات در سمت سرویس متوقف شده است. notification فقط
برای نتیجهٔ action/job (نه polling) و با coalescing محدود ارسال می‌شود تا
rate limit مشاهده‌شده در Plasma تکرار نشود.

**حالت stopped.** `status` در stopped معتبر است (`ready=true`،
`listeners_open=false`). tray هدر «Stopped · listeners closed» نشان می‌دهد و
actionهای pool/probe/rules mutation و proposalها را غیرفعال می‌کند (diagnostics
و probe در stopped اجرا نمی‌شوند)؛ read-onlyها مثل status/rules list باز می‌مانند.
`Set/Re-apply desktop proxy` در stopped غیرفعال است (listenerی نیست که آدرسش
اعمال شود)؛ `Restore` مجاز و برای پاک‌کردن یک setting قدیمی مفید است. اگر
`resume_requires_restart=true` باشد، mode تغییر تا **Restart engine** غیرفعال
است و پس از restart و readiness، tray دوباره mode را فعال می‌کند و
«Stopped · choose a mode to resume» نشان می‌دهد. اگر `false` باشد (process تازه
با state ذخیره‌شدهٔ stopped)، mode مستقیماً فعال است. این flag از `status`
می‌آید و با restart خود tray از بین نمی‌رود؛ نبود فیلد در سرویس قدیمی
«نامعلوم» است و محافظه‌کارانه restart پیشنهاد می‌شود. restart دستی از budget
restart خودکار supervisor مصرف نمی‌کند. پیش از pending برای `Stop listening` و
`Restart engine` (و Quit)، وضعیت desktop با یک `desktop.status` تازه و deadline
کوتاه خوانده می‌شود؛ اگر `applied=true` باشد، label تأیید هشدار می‌دهد که
پروکسی دسکتاپ تا بازگشت listener قطع می‌شود و restore/keep همچنان تصمیم
جداگانهٔ Quit است. اگر خواندن وضعیت ناموفق بود، هشدار «نامعلوم» نمایش داده
می‌شود و action مخفی نمی‌شود.

**Quit و پروکسی دسکتاپ.** پیش از Quit، tray یک `desktop.status` تازه با
deadline کوتاه می‌گیرد. اگر `applied=true` باشد، Quit به دو گزینه تبدیل می‌شود:
«Restore desktop proxy and quit…» (اول `desktop.restore`؛ اگر شکست خورد Quit
انجام نمی‌شود و خطا نمایش داده می‌شود) و «Quit and keep desktop proxy…» (با
هشدار که اینترنت ممکن است تا restore قطع بماند). اگر وضعیت نامعلوم باشد، همان
هشدار با گزینهٔ صریح نمایش داده می‌شود. Quit هرگز به‌طور خودکار تنظیمات دسکتاپ
را restore نمی‌کند. اگر job در حال اجرا باشد، Quit تأیید اضافی می‌خواهد.
ثابت `desktopProxyRestoreHint` قبلی در R15-D2 حذف شد و جای آن همین رفتار
واقعی است؛ شکست `desktop.restore` فقط پیام خطای sanitize‌شدهٔ action را نشان
می‌دهد و هیچ متن راهنمای جداگانه‌ای باقی نمی‌ماند.

**Terminal menu.** آیتم «Terminal menu…» همان executable جاری را با
`[--data-dir <abs>] menu` در یک ترمینال باز می‌کند. فقط یک allowlist ثابت از
ترمینال‌ها با argv صریح پشتیبانی می‌شود: `konsole -e <exe> … menu`،
`gnome-terminal -- <exe> … menu` و `xterm -e <exe> … menu`. هیچ shell،
`$TERMINAL` تعبیرشده یا ورودی کاربر در argv قرار نمی‌گیرد. فرآیند ترمینال با
`Setsid` detached و مستقل از tray است. اگر ترمینالی پیدا نشد، notification
دستور دقیق برای اجرای دستی می‌دهد؛ اگر notification با این دستور از سقف ۱۲۰
rune بیشتر شود، میان آن با `…` کوتاه می‌شود تا executable و پسوند اجرایی `menu`
حفظ شوند. این مسیر برای عملیات متنی و restore بکاپ است و هیچ secret/URLی به tray
داده نمی‌شود.

**امنیت خروجی و D21.** هر label پویا (نام node، rule note، نام source، دلیل
خطا و…) پیش از نمایش از `SanitizeTerminal` و یک truncation ثابت عبور می‌کند؛
سقف label آیتم **۸۰ rune** و سقف هدر/notification **۱۲۰ rune** است و متن
بریده‌شده با `…` مشخص می‌شود. CR/LF/TAB، ESC/C0/C1 و کنترل‌های bidi حذف
می‌شوند. actionها یک enum بسته‌اند و
label هرگز منبع id فرمان نیست. notificationها فقط شمارنده، mode، id کوتاه،
مسیر log یا پیام خطای sanitize‌شده دارند و هیچ مقصد، host مقصد/exit، share URL
یا credentialی در آن‌ها نیست. logهای tray فقط نام action و outcome را دارند، نه
label/input. منوی rules/proposals مقصدها را به‌عنوان نمای زندهٔ حافظه نشان
می‌دهد (D21) و هیچ‌جا persist نمی‌کند؛ backup/state/launch log مسیر متفاوتی
دارند. منو روی session bus قابل خواندن برای همان کاربر است و مرز ذخیره‌سازی
نیست؛ رویدادهای dbusmenu هم از هر فرآیند همان کاربر روی همان bus قابل ارسال‌اند،
پس این سطح فراتر از trust مرز same-uid موجود نیست و ادعای احراز هویت اضافه
نمی‌کند؛ هیچ secret جدیدی فراتر از داده‌ای که state/CLI همان کاربر نشان می‌دهد
از این مسیر افشا نمی‌شود.

**خارج از دامنه.** GUI، i18n، مدیریت service/systemd، restore بکاپ دلخواه،
افزودن/import pool و rule از داخل tray، diagnose با ورودی متن، `exec`،
تغییر DNS/TUN/UDP و هر actionی که نیازمند credential یا URL است. GNOME بدون
extension/SSH همچنان tray ندارد و رفتار D26 تغییر نمی‌کند.

### ۷.۴ امنیت خروجی

- هیچ فرمانی credential، share link، URL منبع با query/token، بدنهٔ درخواست، هدر یا
  کوکی چاپ نمی‌کند. `doctor` مسیر/نام فایل و وضعیت مجوز را نشان می‌دهد (برای لاگ، مسیر
  resolved) و محتوای فایل را نمی‌خواند.
- هر رشتهٔ پویا (نام node، label منبع، note قانون، متن خطای engine/exit) پیش از نمایش
  از `text.SanitizeTerminal` می‌گذرد: حذف C0/C1 control، ESC sequence، CR/LF/TAB
  (تبدیل به فاصله)، کاراکترهای جهت‌دهی یونیکد (`U+202A–202E`, `U+2066–2069`)،
  بریدن به `MaxNodeNameRunes = 120`.
- **شاهد:** `huh v1.0.0` رشته را دست‌نخورده رندر می‌کند؛ اندازه‌گیری: عنوان حاوی
  `ESC[2J ESC[H` در خروجی `lipgloss`/huh دیده شد و `field_select.go` عنوان را از
  `styles.Title.Render(wrap(...))` می‌فرستد. پس کتابخانه محافظ نیست و sanitizer مرز ما
  است؛ `T5` باید با نام node حاوی ESC/CR ثابت کند فریم و انتخاب تغییر نمی‌کند.
- `env`/`exec`: `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY` به listener loopback و
  `NO_PROXY` محدود و قابل توضیح تزریق می‌شوند؛ `NO_PROXY` ارث‌رسیده نمی‌تواند قانون
  تأییدشده را دور بزند. توقف/خرابی listener مجوز اجرای مستقیم نیست.

### ۷.۵ مدل اجرا، tray، autostart و service (D26)

این بخش قرارداد مدل اجرای تصمیم‌شده در D26 است. R13 در درخت کامل شده است
(R13-F verdict **Ready**) و `v1.0.0` همین مدل را منتشر کرده است؛ بسته‌های
`v0.1.3` و پیش از آن همان `run` foreground و unit قدیمی را دارند. **R15 (D28)**
افزونهٔ کنترل tray و restart سطح process است؛ R15-A..C (اسپایک منو،
controller/model و restart در supervisor)، اتصال واقعی tray در R15-D2 و
دامنه‌های R15-E..G2 (pool، rules/proposals، desktop/diagnostics/data/settings و Terminal
menu/Quit) پیاده شده‌اند و منوی controller جای سه آیتم ثابت را گرفته است.
پذیرش R15-H در ۲۰۲۶-۱۱-۲۶ روی data-dir ایزوله اجرا شد و شاهد آن در
`spike/tray/evidence/r15h-acceptance-2026-11-26.md` است؛ بازبینی مستقل، commit
و تصمیم انتشار مراحل جدا هستند.

**مسیرهای canonical instance.** همهٔ فایل‌های هماهنگی instance — `state.lock`،
`app.lock`، `app.owner` و management socket — فقط از دایرکتوری state/data مشتق
می‌شوند: `InstanceDir = <StateDir>/run` (و با `--data-dir`: `<data-dir>/run`)،
مستقل از `XDG_RUNTIME_DIR`. دایرکتوری `0700` و socket `0600` است. این کار desktop
و SSH/headless را با `XDG_RUNTIME_DIR` متفاوت به همان lock/socket canonical
می‌رساند؛ service unit و readiness check هم همان socket را می‌بینند.

**پنجرهٔ migration lock.** برای یک نسخه (R13)، engine پس از گرفتن
`InstanceDir/state.lock` و پیش از هر کار دیگری، lockهای legacy همان instance را
probe/نگه می‌دارد. این مجموعه به data directory انتخابی وابسته است، نه به کاربر:

- با `--data-dir <abs>`: فقط `<abs>/state.lock` و socket متناظر
  `<abs>/run/control.sock`؛ هیچ lock سراسری زیر `/run/user/<uid>` یا
  `$XDG_RUNTIME_DIR` گرفته نمی‌شود تا instanceهای data-dir دیگر و instance
  پیش‌فرض مستقل بمانند (R-19).
- بدون `--data-dir`: `<StateDir>/state.lock` (چیدمان بدون runtime) به‌علاوهٔ
  runtimeهای شناخته‌شدهٔ همان کاربر که R12 برای default استفاده می‌کرد:
  `$XDG_RUNTIME_DIR/routelatch/state.lock` اگر set باشد و
  `/run/user/<uid>/routelatch/state.lock` اگر متفاوت/قابل دسترسی است.

مسیرها ابتدا absolute/Clean می‌شوند (بدون `EvalSymlinks`) و هر مسیر با `lstat`
بررسی می‌شود؛ symlink/non-regular/مالک دیگر بدون follow رد می‌شود. سپس هر مسیر
با `O_CREAT|O_NOFOLLOW` و `0600` باز و با `fstat` همان fd بر اساس `(dev,ino)`
dedupe می‌شود؛ هیچ `stat`/`EvalSymlinks` پیرو برای dedupe استفاده نمی‌شود. اگر
یکی در اختیار
دیگری باشد — حتی engine قدیمی در mode `stopped` — همه آزاد و exit `4` با پیام
«نمونهٔ قدیمی RouteLatch را stop/upgrade کنید» داده می‌شود. اگر مسیر legacy وجود
دارد ولی بازکردنش (مثلاً به‌خاطر مجوز) ممکن نیست، socket متناظر با deadline
کوتاه dial می‌شود و پاسخ‌گویی آن هم exit `4` است؛ اگر نه lock و نه socket قابل
بررسی نباشد، startup برای تضعیف‌نشدن بی‌صدای invariant با خطای روشن و exit `6`
refuse می‌شود. نگه‌داشتن این lockها مانع شروع engine قدیمی (unit فراموش‌شده) هم
می‌شود. **محدودیت:** engine قدیمی default با `XDG_RUNTIME_DIR` سفارشی و
غیرقابل‌حدس دیده نمی‌شود؛ migration همچنان stop صریح unit قدیمی را لازم می‌دارد
(R-20). این سازگاری فقط برای پنجرهٔ migration است و پس از یک نسخه حذف می‌شود.

**`run` پیش‌فرض (حالت اپ).** launcher فقط supervisor را detached می‌کند و از طریق
pipe منتظر readiness می‌ماند؛ **supervisor** (نه launcher و نه engine child) در
عملیات عادی مالک `InstanceDir/app.lock` و یک `flock(LOCK_EX)` دوم روی
`InstanceDir/app.owner` است و در عملیات عادی هر دو را تا خروج نگه می‌دارد؛
استثنای fail-closed در ادامه توضیح داده شده است.
`app.owner` یک regular file پایدار است: اگر وجود ندارد یک‌بار با
`O_CREAT|O_EXCL|O_NOFOLLOW` و `0600` ساخته می‌شود و در اجراهای بعدی همان inode با
`O_NOFOLLOW` باز می‌شود. رکورد آن شامل `state=starting|ready|stopping`، `generation`،
`pid`، `started_at`، `data_dir` مطلق، `listeners` و `log_path` است. هر نوشتن با
`ftruncate(0)`، `pwrite` کل رکورد در offset صفر و `fsync` انجام می‌شود؛ رکورد
دقیقاً یک `\n` پایانی دارد (compact JSON + یک newline). خواننده فقط رکوردی را
می‌پذیرد که JSON کامل و معتبر با همان یک newline پایانی باشد؛ نبود newline،
بایت اضافه یا بریدگی → `starting`/نامعتبر.

ترتیب startup: supervisor `app.lock` را می‌گیرد؛ سپس `app.owner` را
باز/می‌سازد و **پیش از گرفتن flock آن** رکورد `starting` را می‌نویسد تا هیچ
`ready` قبلی معتبر نماند؛ بعد `flock(LOCK_EX)` را با retry کوتاه می‌گیرد و
**در عملیات عادی** تا خروج نگه می‌دارد (استثنای fail-closed در ادامه). سپس
engine lock/tray را preflight می‌کند، engine child را با
`CLOEXEC` (بدون ارث‌بردن lockها) اجرا می‌کند، هویت `(pid, starttime)` خودش و
engine و هر sidecar شناخته‌شده را برای teardown نگه می‌دارد و از طریق pipe
readiness گزارش می‌کند (هیچ فرزندی نباید بدون گزارش هویت از process group خارج
شود)، و فقط پس از
اینکه management socket `status.ready=true` داد **و** tray ثبت شد، رکورد را به
`ready` تغییر می‌دهد؛
listenerها فقط اگر mode ذخیره‌شده `stopped` نباشد لازم‌اند. اگر نوشتن یا گرفتن
flock شکست بخورد، startup abort و lockها آزاد می‌شوند.

**transitionهای readiness.** readiness اپ event-driven و با repair loop سریال
است، نه فقط لحظهٔ startup. پیش از هر stop/restart engine child، از دست رفتن
tray، یا شروع Quit/shutdown، supervisor ابتدا `starting` (در حال restart) یا
`stopping` (در حال خروج) را می‌نویسد و سپس child/tray را تغییر می‌دهد؛ `ready`
دوباره فقط پس از `status.ready=true` کودک جدید **و** ثبت tray نوشته می‌شود. اگر
مرگ غیرمنتظرهٔ child دیده شد، handler پیش از restart همان `starting` را
می‌نویسد. اگر نوشتن این transition شکست بخورد، supervisor پیش از هر کار دیگری
fd/flock `app.owner` را می‌بندد (فایل را حذف نمی‌کند) تا هیچ `run` هم‌زمانی با
lock-success به `0` نرسد؛ **`app.lock` را تا پایان توقف child/tray نگه می‌دارد**
و تنها پس از آن آزاد می‌کند و خارج می‌شود. این تنها استثنای «نگه‌داشتن تا خروج»
است: در این پنجره supervisor زنده است ولی `app.owner` را ندارد؛ `app.lock` هنوز
singleton است و `run` جدید ابتدا همان را می‌بیند، پس شروع نمی‌کند و با
`app.owner` آزاد هم فقط `6` می‌گیرد. بنابراین حتی اگر نوشتن `stopping` شکست
بخورد، stale `ready` بدون owner lock باقی می‌ماند و `run` بعدی `6` می‌گیرد.

**restart سطح process از tray (مسیر supervisor در R15-C و اتصال واقعی tray در R15-D2 پیاده شدند).** tray برای
`Restart engine` فقط پیام `restart-engine` را در pipe supervisor می‌نویسد؛ این یک lifecycle request است،
نه management op. supervisor در پردازش آن همان ترتیب transition را نگه می‌دارد:
`starting` نوشته می‌شود، engine child فعلی با bounded teardown متوقف می‌شود،
child تازه با همان مسیر `run --headless` و همان data-dir اجرا می‌شود، سپس
`status.ready` کودک جدید **و** ثبت tray دوباره بررسی و `ready` نوشته می‌شود.
اگر نوشتن/آمادگی شکست بخورد، مسیر fail-closed یا exit مستند موجود اجرا می‌شود؛
حلقهٔ restart پنهانی اضافه نمی‌شود. اگر engine در حال آماده‌شدن یا restart
خودکار باشد، درخواست restart دستی نادیده گرفته می‌شود (tray هم action را در
این حالت غیرفعال می‌کند). restart دستی از سقف restart خودکار خرابی مصرف نمی‌کند
و tray درخواست‌ها را سریال می‌کند. اگر `quit` حین restart برسد،
پس از پایان/شکست restart پردازش می‌شود. restart درون‌process همچنان ممنوع است.
**کانال کنترل دوطرفه.** برای اینکه tray پایان restart را با `ready` کهنهٔ
engine قبلی اشتباه نگیرد، کانال tray/supervisor باید علاوه بر پیام‌های
tray→supervisor رویدادهای supervisor→tray را هم حمل کند (socketpair یا pipe
دوم؛ انتخاب مکانیکی فنی است): `restarting` هنگام شروع و `ready` فقط پس از
`status.ready` کودک جدید و ثبت tray. همین رویدادها برای restart خودکار
(مرگ/خطای engine) هم ارسال می‌شوند تا tray وضعیت را حدس نزند. job restart در
tray فقط پس از دیدن `restarting` و سپس `ready` موفق اعلام می‌شود؛ اگر رویداد
نرسد، timeout و «unconfirmed» گزارش می‌شود، نه موفقیت. polling همان status
فقط fallback تشخیصی است، نه جایگزین event. نوشتن رویدادها bounded/non-blocking
است؛ tray کند یا مرده نباید supervisor را بلوکه کند و در صورت گم‌شدن رویداد،
polling status و timeout همان مسیر تشخیصی‌اند. tray child مسیر socket را از
`TrayOptions.Socket` (مشتق از `store.Paths.Socket`) می‌گیرد و برای عمل‌های
مدیریتی همان `control.Client` را صدا می‌زند؛ هیچ فایل state/config را مستقیم
نمی‌خواند یا نمی‌نویسد.

**liveness و `already_running`.** singleton را `app.lock` تعیین می‌کند و
readiness/owner را `app.owner`؛ هیچ‌کدام به `pid` تکیه نمی‌کنند. گرفتن موفق
flock `app.owner` به‌تنهایی هرگز مجوز شروع یا `0` نیست، چون ممکن است در پنجرهٔ
fail-closed باشد؛ `app.lock` اول بررسی می‌شود. supervisor هر چند ثانیه (پیشنهاد
۱۰s) وجود/اعتبار `app.owner` را با `lstat` و
مقایسهٔ inode با fd خودش بررسی می‌کند:

- اگر فایل همان inode قفل‌شده است، state/listeners رکورد را با فاز فعلی مقایسه
  می‌کند و محتوای ناقص/خراب **یا رکورد معتبری که با فاز نمی‌خواند** (مثلاً
  `ready` در حالی که child/tray آماده نیست یا Quit شروع شده) را با نوشتن کامل
  **state فعلی** بازنویسی می‌کند (fd و flock دست‌نخورده): `starting`/`stopping`
  تا برقراری دوبارهٔ readiness، و فقط پس از آن `ready`.
- اگر مسیر حذف شده است، یک inode تازه با `O_CREAT|O_EXCL|O_NOFOLLOW` می‌سازد،
  **دقیقاً state فعلی** (`starting`، `stopping`، یا `ready` فقط اگر readiness
  همین حالا برقرار است و فاز `stopping` نیست) را می‌نویسد، `flock(LOCK_EX)` را
  با retry کوتاه می‌گیرد، دوباره state فعلی را می‌نویسد و **تنها پس از قفل‌شدن
  inode تازه** fd قدیمی را می‌بندد. در فاز `stopping` حتی اگر engine هنوز
  `status.ready=true` بدهد، هرگز `ready` نوشته نمی‌شود. ترتیب
  create→write→flock→write→close-old باید حفظ شود تا هرگز مسیر بدون flock یا
  `ready` زودهنگام نماند؛ در فاصلهٔ کوتاه بین حذف و قفل تازه، `run` جدید `6`
  می‌گیرد.
- اگر مسیر به symlink/non-regular/مالک دیگر تبدیل شده یا inode آن با fd فرق
  دارد (جایگزینی/بازسازی توسط دیگری)، adopt/overwrite نمی‌کند و fail-closed
  می‌شود.
- اگر نوشتن یا گرفتن flock روی inode تازه سه بار متوالی شکست بخورد، supervisor
  برای اینکه appی بدون marker قفل‌شده نماند fail-closed متوقف می‌شود، child را
  می‌بندد و lockها را آزاد می‌کند (دلیل `app_marker_invalid` یا
  `app_marker_write_failed`).

در طول startup، repair هرگز `ready` نمی‌نویسد؛ اگر marker حذف/خراب شود، `run`
جدید تا برقراری readiness اپ `6` می‌گیرد و پس از transition عادی به `ready` (یا
repair بعدی) `0` می‌شود. پس از یک چرخهٔ repair موفق در حالت ready، `run` بعدی
باید `0` بگیرد (نه `6`).

`run` جدید: اگر `app.lock` آزاد است آن را می‌گیرد و ادامه می‌دهد. اگر در اختیار
دیگری است، `app.owner` را با `O_NOFOLLOW` باز می‌کند (نبودن فایل → exit `6`
«repair pending»؛ مسیر غیرمجاز → exit `6` `app_marker_invalid`) و
`flock(LOCK_EX|LOCK_NB)` را امتحان می‌کند:

- گرفتن موفق یعنی هیچ supervisor زنده‌ای مالک marker نیست و محتوای آن stale
  است؛ فوراً آزاد می‌شود و خروجی **همیشه `6`** است، هرگز `0`.
- شکست یعنی supervisor زنده است. رکورد باید کامل، `state=ready` و `data_dir`
  برابر data-dir فعلی باشد؛ سپس یک **negative readiness check** کوتاه روی
  management socket انجام می‌شود: اگر `status` در دسترس نباشد یا `ready=false`
  باشد، exit `6` است. فقط اگر marker `ready` باشد و socket هم آن را رد نکند،
  `ok=true`/`already_running=true`/exit `0` با `pid`/`listeners`/`log_path`
  همان رکورد برمی‌گردد. `starting`/`stopping`، رکورد ناقص/نامعتبر یا `data_dir`
  متفاوت → exit `6`.

management socket هرگز به‌تنهایی `0` نمی‌سازد (ثبت tray را ثابت نمی‌کند)؛ فقط
می‌تواند یک `ready` ظاهراً معتبر را **veto** کند. ترکیب «flock زنده + marker
ready + socket.ready» پنجرهٔ child مرده/Quit/restart را می‌بندد. حذف مؤقت marker
حداکثر یک چرخهٔ repair (۱۰s) خروجی `6` می‌دهد، نه `0` کاذب؛ marker به‌طور دائمی
غایب نمی‌ماند، چون supervisor آن را repair می‌کند و در شکست پایدار fail-closed
می‌شود.

اگر lock را گرفتیم ولی engine lock در اختیار یک `--headless` است، lockها آزاد و
**صرف‌نظر از marker** exit `4` با پیام «ابتدا نمونهٔ headless را متوقف کنید یا
`--headless` اجرا کنید» داده می‌شود؛ نبود tray/StatusNotifier → lockها آزاد و
`unsupported`/exit `7` با دلیل پایدار `tray_unavailable` و پیشنهاد `--headless`؛
سپس شروع. پس از گرفتن lock، marker کهنه (حتی با `data_dir` متفاوت) با نوشتن
`starting` بازنویسی می‌شود؛ اما symlink/non-regular/مالک دیگر دنبال یا overwrite
نمی‌شود.

**timeout و دو `run` هم‌زمان.** launcher readiness deadline دارد (پیشنهاد ۱۰s).
supervisor فرزند مستقیم launcher است (بدون double-fork) تا `waitpid` معنا داشته
باشد. **پیش از spawn**، launcher با `pidfd_open` روی PID خودش توانایی pidfd را
probe می‌کند؛ `ENOSYS`/`EPERM` یعنی pidfd مجاز نیست و `run` پیش‌فرض با
`unsupported`/exit `7` و دلیل `pidfd_unavailable` refuse می‌کند و هیچ processی
نمی‌سازد (حالت `--headless` وابسته به pidfd نیست). سپس supervisor را spawn
می‌کند و pidfd آن را **بلافاصله** باز می‌کند و تا پایان گروه‌scan نهایی نگه
می‌دارد؛ این pidfd تا بسته‌نشدن، PID/PGID supervisor را از reuse محافظت می‌کند.
اگر بازکردن pidfd supervisor پس از spawn شکست بخورد، چون فرزند مستقیم و
reap‌نشده است PID آن قابل reuse نیست: launcher با SIGKILL روی همان PID فرزند
مستقیم آن را می‌بندد، `waitpid` می‌کند و با exit `1` برمی‌گردد؛ `ESRCH` یعنی
supervisor از قبل مرده و همان failure startup/exit `1` گزارش می‌شود، نه `7`.

supervisor هویت خود و هر فرزند (engine/sidecar) را به‌صورت `(pid, starttime)` در
pipe readiness گزارش می‌کند؛ هیچ فرزندی نباید بدون گزارش از process group خارج
شود. launcher برای هر گزارش، pidfd را **فوراً** باز می‌کند:

- `ESRCH` یعنی فرزند از قبل مرده است؛ ثبت می‌شود و ادامه می‌دهد.
- `ENOSYS`/`EPERM`/خطای دیگر یعنی سیگنال ایمن ممکن نیست؛ به PID/PGID bare سیگنال
  فرستاده نمی‌شود. launcher با pidfd supervisor خودش teardown را آغاز می‌کند
  (SIGTERM/SIGKILL و `waitpid`) و سپس فرزندان/گروه را بررسی می‌کند؛ خروجی
  `teardown_incomplete`/exit `1` است، اما app تا حد امکان باقی نمی‌ماند.
- پس از باز شدن pidfd، starttime همان processِ pin‌شده از `/proc/<pid>/stat`
  خوانده و با مقدار گزارش‌شده مقایسه می‌شود؛ mismatch/خطا یعنی هویت قابل تأیید
  نیست و همان مسیر abort/exit `1` بالا اجرا می‌شود.

همهٔ سیگنال‌ها فقط با `pidfd_send_signal` روی pidfdهای بازشده فرستاده می‌شوند.
**تنها استثنا:** اگر pidfd supervisor پس از spawn باز نشود، `SIGKILL` به همان
PID فرزند مستقیم و reap‌نشدهٔ launcher مجاز است، چون تا `waitpid` تخصیص PID آن
آزاد نمی‌شود و reuse ممکن نیست؛ این مسیر بلافاصله `waitpid` می‌کند. به هیچ
PID/PGID دیگری سیگنال خام فرستاده نمی‌شود. `pidfd_send_signal` با `ESRCH` یعنی process
از قبل مرده و ادامه می‌دهد؛ `EPERM`/خطای دیگر یعنی بدون سیگنال و
`teardown_incomplete`/exit `1`.

در timeout، pipe readiness بسته می‌شود، به supervisor با pidfd خودش `SIGTERM`
فرستاده می‌شود، تا ۵s برای خروج supervisor صبر می‌شود و در صورت نیاز `SIGKILL`
می‌شود. launcher با `waitpid` خروج supervisor را تأیید می‌کند (pidfd supervisor
باز می‌ماند). سپس برای هر فرزند گزارش‌شده: اگر هنوز زنده/غیرzombie است،
SIGTERM با pidfd، انتظار محدود برای exit/zombie، و در صورت نیاز SIGKILL با pidfd؛
تا زمانی که pidfd آن قابل خواندن (exit) نشود یا `/proc` وضعیت `Z`/غیرموجود
نشان ندهد، ادامه می‌دهد. اگر هر فرزند گزارش‌شده پس از deadline هنوز زنده/غیرzombie
باشد، exit `1`/`teardown_incomplete` است و هرگز `8` برنمی‌گردد.

فقط پس از مرگ/zombie شدن همهٔ فرزندان گزارش‌شده، launcher با supervisor pidfd
هنوز‌باز، `/proc` را برای اعضای process group قدیمی اسکن می‌کند و هر عضو
زنده/غیرzombie را با مجموعهٔ `(pid,starttime)` گزارش‌شده مقایسه می‌کند؛ تطابق
یعنی همان فرزند هنوز زنده است و عدم‌تطابق یعنی عضو ناشناخته/reused. در هر دو
حالت زنده‌بودن تخلف است: به آن سیگنال نمی‌فرستد و `teardown_incomplete`/exit `1`
می‌دهد. اگر گروه خالی (فقط zombie) بود، pidfd supervisor بسته و exit `8` داده
می‌شود. processهای zombie با وضعیت `Z` باقی‌مانده حساب نمی‌شوند.

exit `8` فقط یعنی «همهٔ processهای گزارش‌شده و اعضای تأییدشدهٔ process group همین
invocation دیگر زنده نیستند»؛ آزادبودن global `app.lock` شرط نیست، چون یک `run`
هم‌زمان می‌تواند پس از آزادشدن آن app جدید بسازد. دو `run` هم‌زمان: دومی با
`app.lock` سریال می‌شود و طبق بالا exit `6`/`0` می‌گیرد. `--json` در حالت
پیش‌فرض یک envelope زمان startup با `pid`، listenerها، log path و
`already_running?` یا `state/reason` می‌دهد؛ `--headless --json` رفتار
blocking/shutdown فعلی را دارد. log/نوتیفیکیشن tray نباید مقصد، credential یا URL
منبع داشته باشد (D21).

`--headless` هیچ‌گاه tray را باز نمی‌کند و داخل systemd/ترمینال بلاک می‌ماند؛ unitها
باید `--headless` داشته باشند. plain `run` عمداً detach می‌شود و برای unit مناسب
نیست؛ هیچ heuristic مبتنی بر `INVOCATION_ID` برای رد کردن آن لازم نیست (این متغیر
می‌تواند به فرزندان session گرافیکی هم ارث برسد). Quit یا SIGTERM supervisor باید
child را با SIGTERM متوقف کند، تا پایان shutdown صبر و سپس در صورت نیاز kill/reap
کند؛ مرگ غیرمنتظرهٔ supervisor هم باید child را متوقف کند (parent-death/watchdog),
تا listener/management/پروکسی یتیم نماند.

**`autostart enable|disable|status`.** فقط Linux/XDG؛ فایل در
`$XDG_CONFIG_HOME/autostart/routelatch.desktop` (fallback `~/.config/autostart/...`)
با `Type=Application`، `Name=RouteLatch`، `Comment`، `Exec="<absolute>" run` (با
quoting صحیح Desktop Entry برای مسیر دارای فاصله)، `Terminal=false`،
`NoDisplay=true` و marker مالکیت (`X-RouteLatch-Autostart=true`) نوشته می‌شود.
`enable` idempotent است: فایل مالک با محتوای درست را بازنویسی/restart نمی‌کند؛
فایل مالکِ کهنه را با محتوای جدید update می‌کند؛ فایل موجود غیرمالک را overwrite
نمی‌کند و exit `6` می‌دهد. `enable` خود اپ را start نمی‌کند و `disable` اپ در حال
اجرا را stop نمی‌کند؛ فقط entry ورود بعدی را تغییر می‌دهند. `disable` فقط فایل مالک
را حذف می‌کند و برای فایل غیرمالک exit `6` می‌دهد؛ `status` فقط خواندنی است،
`managed` را نشان می‌دهد و اگر managed service هم وجود داشته باشد با
`conflict=service` و exit `0` هشدار می‌دهد (setup commands در این حالت exit `6`).
`status` فیلدهای `tray_available` و `pidfd_available` را هم نشان می‌دهد و `doctor`
همین دو check را دارد. `enable` پیش از نوشتن **یا** اعلام idempotent، هر دو را
probe می‌کند: نبود StatusNotifier host → `unsupported`/exit `7` با دلیل
`tray_unavailable`؛ نبود pidfd مجاز (ENOSYS/EPERM) → `unsupported`/exit `7` با
دلیل `pidfd_unavailable` (R-21). autostart اصلاً حالت headless ندارد و فقط
instance پیش‌فرض را مدیریت می‌کند: هر سه فرمان با `--data-dir` با exit `2` رد
می‌شوند و برای data-dir سفارشی `service install --data-dir` مسیر درست است. اگر managed
service unit وجود داشته باشد، `enable` برای جلوگیری از دو supervisor هم‌زمان
exit `6` می‌دهد. نصب بسته هیچ فایل autostart نمی‌سازد.

**`service install|uninstall|status`.** فقط Linux با systemd user manager. `install`
unit را در base config **همان user manager** می‌نویسد:
`<manager-config-base>/systemd/user/routelatch.service`؛ manager base از
`systemctl --user show-environment` (`XDG_CONFIG_HOME` و در نبودش `HOME/.config`)
resolve می‌شود، نه صرفاً از محیط shell. اگر این base قابل تعیین/تأیید نباشد،
`install` پیش از نوشتن با `unsupported`/exit `7` یا `6` refuse می‌کند تا unit در
مسیری که systemd نمی‌بیند نوشته نشود. `uninstall`/`status` هم همان manager base
را resolve می‌کنند، نه صرفاً محیط shell را. فایل یک marker مالکیت دارد
(`# Managed by RouteLatch service install`). محتوای unit:
`Type=simple`، `ExecStart=<absolute> [--data-dir <abs>] run --headless`،
`Restart=on-failure`، `RestartSec=2s`،
`RestartPreventExitStatus=1 4 6 7 9 10`، `KillSignal=SIGTERM`،
`KillMode=mixed`، `TimeoutStopSec=20s`، `WantedBy=default.target`. unit هیچ
`User=`/`Group=` ندارد و root لازم نیست.
`install` baseهای داده را از محیط فراخوان resolve می‌کند: `XDG_CONFIG_HOME`
(پیش‌فرض `~/.config`) و `XDG_STATE_HOME` (پیش‌فرض `~/.local/state`) — خودِ base،
نه `<base>/routelatch` — و وقتی `--data-dir` نیست آن‌ها را به‌صورت absolute با
`Environment="XDG_CONFIG_HOME=<base>" "XDG_STATE_HOME=<base>"` در unit pin
می‌کند (quoting/escape طبق systemd، از جمله `%` → `%%`) تا service و readiness
check مستقل از محیط shell و `XDG_RUNTIME_DIR` همان
`<state-base>/routelatch/run/control.sock` را ببینند. با `--data-dir <abs>`، همان
مسیر مطلق همهٔ ریشه‌ها را تعیین می‌کند و Environment لازم نیست.
`install` idempotent است: اگر unit نیست، اتمیک می‌نویسد،
`systemctl --user daemon-reload` و `enable --now` می‌زند؛ اگر همان unit هست، بدون
بازنویسی/restart فقط enabled/active را همگرا می‌کند؛ اگر محتوا متفاوت است، بدون
`--force` بازنویسی نمی‌کند و exit `6` می‌دهد. `Type=simple` «active» را زودتر از
آمادگی listenerها اعلام می‌کند، پس install فقط به `ActiveState` تکیه نمی‌کند: تا
یک deadline محدود (پیشنهاد ۱۰s) مدیریت همان data-dir را poll می‌کند و فقط
`status.ready=true` (و در mode غیر `stopped`، `listeners_open=true`) را قبول
می‌کند. اگر child با exit مستند `1/4/6/7/9/10` مرد، install همان را منتقل
می‌کند (1→1، 4→6، 6→6، 7→7، 9→9، 10→10) و rollback اجرا می‌شود؛ این exitها
همگی terminalاند و در `RestartPreventExitStatus` قرار دارند، پس نبود race با
restart خودکار systemd. انقضای deadline با child زنده exit `8` است و rollback
می‌شود؛ `stopped` معتبر بدون listener رد نمی‌شود.
`--force` unit را به‌روزرسانی می‌کند، daemon-reload و enable می‌زند و اگر unit از
قبل active بود restart/try-restart می‌کند؛ اگر inactive بود `enable --now` آن را
start می‌کند. `--force` می‌تواند unit قدیمی/بدون marker را در مسیر هدف، فقط اگر
regular file و مالک کاربر باشد و unit قبلی **inactive/stopped** باشد، adopt و با
marker فعلی جایگزین کند؛ enabled یا disabled بودن قبلی مجاز است و install وضعیت
enabled را طبق قرارداد خود همگرا می‌کند. اگر markerless unit هنوز active باشد،
engine آن «headless دیگر» شمرده می‌شود و install exit `6` می‌دهد. symlink یا non-regular
overwrite نمی‌شود و exit `6` می‌دهد.

پیش از preflight، `install` ابتدا target managed unit را مشخص می‌کند: اگر همان unit
managed خودش active است، engine آن **مجاز** است و install/`--force` همان unit را
همگرا/restart می‌کند؛ lockهای canonical و legacy همان data-dir که در اختیار engine
همین unit هستند تعارض شمرده نمی‌شوند و فقط پس از stop/restart طبیعی آزاد/دوباره
گرفته می‌شوند. فقط اگر app یا headless engine دیگری با همان data-dir در حال اجرا
باشد (یعنی engine مالک این unit نیست؛ شامل lockهای legacy پنجرهٔ migration)،
بدون نوشتن/فعال‌سازی unit با exit `6` refuse می‌کند. اگر managed autostart وجود داشته باشد، `install` هم برای جلوگیری از
دو supervisor exit `6` می‌دهد. `ExecStart` و `--data-dir` با quoting/escape صحیح
systemd نوشته می‌شوند (از جمله `%` → `%%` و `$` → `$$`) تا مسیرهای دارای
فاصله/کاراکتر ویژه معتبر بمانند.

قبل از هر نوشتن، `install` snapshot می‌گیرد: وجود unit قبلی، محتوای آن، enabled،
active، `MainPID`/starttime و نه یک stop-flag از پیش. اگر یکی از مراحل اصلی پس
از نوشتن (`daemon-reload`، `enable`، `start`/`restart`، readiness) شکست بخورد،
دقیقاً یک rollback با precedence صریح اجرا می‌شود:

- **unit قبلی وجود ندارد (نصب تازه):** unit جدید stop/disable و حذف می‌شود.
- **unit قبلی وجود دارد، قبل از install فعال بود، و اکنون هم active است با
  `MainPID` غیرصفر و starttime برابر snapshot:** process را stop نمی‌کند و unit
  را حذف نمی‌کند؛ فایل قبلی را byte-for-byte restore، enabled را به snapshot
  برمی‌گرداند (disable فقط وضعیت boot را عوض می‌کند، نه process) و
  `daemon-reload` می‌زند.
- **unit قبلی وجود دارد ولی شرط بالا برقرار نیست** (قبلاً inactive بود، یا
  `MainPID=0`، یا starttime/PID عوض شده، یا در جریان start/restart متوقف/جایگزین
  شد): unit جدید را stop/disable می‌کند، فایل قبلی را restore و enabled قبلی را
  برمی‌گرداند؛ سپس اگر unit قبلی قبل از install فعال بود آن را start می‌کند و
  اگر قبلاً inactive بود start نمی‌کند.
- **adoption markerless:** فایل قبلی backup می‌شود و در شکست byte-for-byte
  restore؛ چون unit قبلی inactive است، صریحاً شاخهٔ «شرط بالا برقرار نیست»
  (بدون start) اعمال می‌شود، حتی اگر `MainPID=0` با snapshot برابر باشد.

rollback یک‌بار اجرا می‌شود؛ شکست خود آن terminal است: هر دو خطا گزارش و unit برای
بررسی دستی باقی می‌ماند (exit `1`). خروجی برای bind/port مستند `6` و برای شکست
ابزار `1` (`external_tool_failed`) است. اگر `Linger=no` باشد،
فقط دستور `sudo loginctl enable-linger <user>` را چاپ می‌کند و خودش sudo اجرا
نمی‌کند. اجرای فرمان با `SUDO_USER`/`PKEXEC_UID` برای کاربر دیگر refuse می‌شود
(exit `9`)؛ فقط کاربر هدف باید آن را اجرا کند. اگر systemd user manager در دسترس
نباشد، `unsupported`/exit `7` است.

`uninstall` **اول** marker/ownership را بررسی می‌کند: unit غیرمالک را نه stop/disable
می‌کند، نه حذف؛ exit `6` و بدون تغییر. فقط unit مالک را stop/disable، حذف و
daemon-reload می‌کند (idempotent) و state/config/linger را حذف نمی‌کند. `status`
فقط خواندنی است، `managed` را نشان می‌دهد و اگر managed autostart هم وجود داشته
باشد با `conflict=autostart` و exit `0` هشدار می‌دهد (setup commands در این حالت
exit `6`). بسته هیچ unit/postinst ندارد (از R13 به بعد) و این فرمان تنها مسیر ساخت
سرویس است.

**مسیرهای managed.** برای `app.lock`، `app.owner`، فایل autostart و unit سرویس یک
قاعدهٔ واحد است: با `Lstat`/`O_NOFOLLOW` بررسی می‌شوند، باید regular file، مالک uid
جاری و داخل دایرکتوری مورد انتظار باشند؛ symlink/non-regular/مالک دیگر بدون
side-effect رد می‌شود (exit `6`؛ برای `app.owner` عدم دنبال‌کردن + abort startup
با دلیل `app_marker_invalid`، نه overwrite). `app.lock` یک‌بار با
`O_CREAT|O_RDWR|O_NOFOLLOW` و `0600` ساخته می‌شود، fd آن در عملیات عادی تا خروج
supervisor نگه داشته می‌شود و در مسیر fail-closed پس از توقف child/tray و پیش از
خروج آزاد می‌شود؛ RouteLatch هرگز آن را unlink/replace نمی‌کند؛ حذف/جایگزینی دستی آن توسط همان uid
خارج از تضمین single-instance است (root/همان uid مرز امنیتی نیست).
`app.owner` هم یک inode پایدار است: یک‌بار ساخته می‌شود، lock آن در عملیات عادی
تا خروج supervisor نگه داشته می‌شود (در مسیر fail-closed پیش از `app.lock` آزاد
می‌شود) و محتوا در همان fd نوشته می‌شود (نه temp+rename) تا lock و
محتوا از هم جدا نشوند؛ بازسازی پس از حذف/خرابی فقط با همان قاعدهٔ no-follow و
بدون overwrite مسیر غیرمجاز است. `service install` بدون `--force` فقط unit regular/مالک را
همان می‌داند؛ `--force` symlink را overwrite نمی‌کند. `disable`/`uninstall` برای
مسیر symlink/non-regular/غیرمالک هیچ stop/disable/حذفی انجام نمی‌دهند و exit `6`
می‌دهند.

**مرزها و migration.** invariant سخت «یک engine در هر data directory» است، نه هر
کاربر؛ app و service به‌صورت پیش‌فرض همان data-dir XDG را دارند. `--data-dir`
سفارشی یک instance جدا می‌سازد و ممکن است با portهای instance دیگر تصادم کند؛
`service install` فقط lock همان data-dir را preflight می‌کند. `autostart` و
`service` برای یک کاربر هم‌زمان فعال نمی‌شوند؛ اگر هر دو به‌صورت دستی وجود داشته
باشند، `status` باید تعارض را گزارش کند و کاربر باید یکی را انتخاب/حذف کند.
restart همیشه در سطح process است (tray child، supervisor به‌درخواست tray، یا
systemd)؛ restart درون‌process ممنوع می‌ماند. tray supervisor خروج غیرمنتظره
child را با backoff و سقف (پیشنهاد: ۵ بار
در ۶۰ ثانیه) restart می‌کند؛ exitهای مستند 1/4/6/7/9/10 بدون restart گزارش
می‌شوند و child متوقف می‌ماند. exit `1` خطای داخلی terminal است (نه retry) تا
rollback نصب و سیاست restart با هم ناسازگار نشوند؛ systemd هم آن را در
`RestartPreventExitStatus` دارد. پروکسی دسکتاپ همچنان opt-in است و توقف/Quit اپ آن
را خودکار restore نمی‌کند؛ tray باید هشدار/پیشنهاد restore بدهد. GUI آینده همان
`run` پیش‌فرض را GUI+tray می‌کند و فرمان `gui` جدا ندارد. **در وضعیت فعلی (تا
R15-H)** ورودی hint منوی tray فقط `routelatch desktop proxy restore` را پیشنهاد
می‌دهد (notification) و هیچ تنظیم دسکتاپ یا state را تغییر نمی‌دهد؛ restore خودکار
یا مخفی وجود ندارد. **R15 (D28؛ controller در R15-B پیاده‌شده، اتصال واقعی tray در R15-D2)** این hint را با
actionهای واقعی `Set/Re-apply` و `Restore` (هر دو با تأیید دو مرحله‌ای) و
Quit با گزینه‌های `Restore and quit`/`Keep` جایگزین می‌کند؛ Quit همچنان
خودکار restore نمی‌کند.

**Migration از unit فعلی.** قبل از upgrade، unit قدیمی
`systemctl --user disable --now routelatch.service` می‌شود. برای نصب tarball،
فایل markerless در **manager config base**
(`<manager-config-base>/systemd/user/routelatch.service`) و در صورت تفاوت، همان
مسیر legacy محیط shell (`$XDG_CONFIG_HOME/systemd/user/...` یا
`~/.config/systemd/user/...`) و symlinkهای `default.target.wants` باید بررسی و
حذف شوند؛ برای نصب بسته، unit در `/usr/lib/systemd/user/routelatch.service` با
upgrade حذف می‌شود، ولی باید بررسی و در صورت باقی‌ماندن symlink/user unit پاک
شود. سپس `systemctl --user daemon-reload`، نصب بستهٔ جدید و انتخاب
`autostart enable` یا `service install` انجام می‌شود.

اگر unit قدیمی فراموش شود، رفتار به data directory وابسته است:

- **همان data-dir:** engine قدیمی lock legacy همان instance را نگه می‌دارد؛
  engine جدید همان lock را در پنجرهٔ migration می‌گیرد و نمی‌تواند، پس قبل از هر
  bind با exit `4` رد می‌شود — حتی اگر آن engine در mode `stopped` و بدون
  listener باشد. unit فراموش‌شده‌ای که engine جدید را start می‌کند هم چون
  lockهای legacy در اختیار app جدید هستند نمی‌تواند engine دوم بسازد.
- **data-dir متفاوت** (مثلاً unit/engine قدیمی default و app جدید با
  `--data-dir`): دو instance مستقل‌اند (R-19) و ممکن است بر سر port تصادم کنند؛
  migration باید instance قدیمی را صریحاً stop/disable کند. lockهای جدید عمداً
  سراسری نیستند، پس این coexistence را نمی‌بندند.
- **runtime سفارشی:** اگر engine قدیمی default با `XDG_RUNTIME_DIR` سفارشی و
  غیرقابل‌حدس اجرا شده باشد، دیده نمی‌شود (R-20)؛ stop صریح unit قدیمی همچنان
  لازم است.

`service install --force` می‌تواند unit قدیمی markerless را فقط در صورت regular
file/مالکیت کاربر adopt کند؛ بدون `--force` unit متفاوت exit `6` می‌دهد. اسکریپت‌ها
و تست‌های داخلی که foreground می‌خواهند باید `run --headless` را صدا بزنند.
R13-E باید upgrade با unit کهنه را با نتیجهٔ قابل مشاهده بیازمايد: unit قدیمی
stop/disabled می‌ماند، بستهٔ جدید هیچ unit در `/usr/lib` ندارد، هیچ restart loop،
orphan یا direct fallback رخ نمی‌دهد، و مسیر انتخاب‌شده (`autostart` یا `service`)
سالم بالا می‌آید. این آزمون باید engine قدیمی فعال و `stopped` را، با و بدون
`XDG_RUNTIME_DIR`، برای همان data-dir و همچنین حالت default-old + data-dir-new
پوشش دهد و ثابت کند حالت اول engine دوم نمی‌سازد و حالت دوم صراحتاً stop
می‌شود. `run` بدون `--headless` در unit جدید عمداً پشتیبانی نمی‌شود.
## ۸. پوشش پذیرش

| معیار | task | check |
| --- | --- | --- |
| A01 | T6 | نصب پاک Linux amd64/arm64 از tarball یا `.deb` محلی (و اسپک RPM؛ باینری RPM در CI ساخته میشود)، بدون runtime دستی؛ artifact خودکفا |
| A02 | T3 (+T5 برای ورودی قانون) | جدول §۱.۶ و ماتریس §۲.۵؛ domain/suffix/IP/CIDR و خارج از فهرست |
| A03 | T3 | pool خالی/مرده/tag غایب: `pool_unavailable`، صفر تماس مستقیم با مقصد تأییدشده، مقصد مستقیم سالم |
| A04 | T3 | چهار حالت، استثنای محلی، جدول §۲.۶، sweep با counter و `LocalCloseDeadline` |
| A05 | T5 (+T1 برای feedback) | failure واجد شرایط → probe امن → پیشنهاد؛ هیچ rule بدون تأیید؛ disable صف را لغو می‌کند؛ no replay |
| A06 | T4 | import چهار مسیر، sanitization §۵.۴، رکورد insecure با دلیل `insecure_tls_removed` و hint قابل‌مشاهده `unsupported` است؛ refresh شکست‌خورده pool سالم را حفظ می‌کند |
| A07 | T4 | رتبه/cooldown/آخرین موفق بدون restart؛ staging و rollback؛ fence نسخه و tag پایدار per record |
| A08 | T3 | بدون MITM/DNS سیستمی؛ hostname بدون resolve تا exit؛ محدودیت resolver مصرف‌کننده مستند |
| A09 | T2–T6 | loopback-only، socket `0600` + peer check، فایل `0600`، لاگ/خروجی redacted، sanitizer ترمینال |
| A10 | T5 | جدول §۷.۱ کامل؛ منو معادل؛ JSON و exit codeها تست‌شده |
| A11 | T6 | GNOME/KDE set/restore با ownership؛ unsupported صریح؛ bypass پنهانی نیست؛ restore آفلاین |
| A12 | T3 | status/redirect/body/stream بایت‌به‌بایت؛ HEAD/POST/body؛ UDP صریحاً `0x07` بدون سوکت UDP |
| A13 | R13 | `run --headless` foreground، crash/port collision، config خراب، shutdown تمیز با دادهٔ سالم؛ restart فقط در سطح process |
| A14 | T7 — **D20 بسته شد؛ انتشار باز** | D20 با Apache-2.0 بسته شد و ممیزی واقعی باینری (۶۸ ماژول لینک‌شده + ۶ کامپوننت embedشده، ۰ مجوز ناشناخته) در [`licensing.md`](licensing.md) ثبت است؛ باقی‌مانده: source offer برای MPL، بستهٔ انتشار/سورس، مجوز انتشار و بازبینی ادعای امنیت/نام |
| A15 | R13 | `run` پیش‌فرض detached/tray فقط بعد از readiness واقعی (`status.ready` و listenerهای لازم بر اساس mode) برمی‌گردد؛ `--headless` foreground؛ `autostart`/`service install` idempotent؛ بسته بدون unit/postinst؛ نبود tray/pidfd fail صریح (`7`)؛ teardown فقط با هویت تأییدشده (survivor → `1`، clean → `8`)؛ migration از lock/unit قدیمی engine دوم نمی‌سازد |
| A17 | R16 | پوشش TUN طبق §۱۰: helper یک‌باره/بدون root engine؛ `tun on` فقط non-stopped؛ dual-stack؛ TCP به همان router/pool با sniffing؛ DNS سیستم بدون تغییر و plain DNS مستقیم؛ UDP غیرDNS بلاک؛ fail-closed در قطع engine؛ ترتیب stop/Quit؛ ماندگاری و auto-enable با warning؛ آیکن/منوی tray؛ صفر مقصد در log/state |

## ۹. محدودیت‌های ثبت‌شده و کار باز

| ID | محدودیت | چه چیزی آن را می‌بندد |
| --- | --- | --- |
| R-1 | کدهای SOCKS5 محدود به `0x03/0x04/0x05/0x07/0x08`؛ `0x01`/`0x02` تولید نمی‌شود | تست T3 روی کد سیم؛ در صورت نیاز، dispatch خودمان در milestone بعد |
| R-2 | HTTP CONNECT exit در T1 اجرا نشد؛ بدون آن `ProofConfirmed` برای HTTP قابل ادعا نیست | T3 با exit محلی کنترل‌شده؛ تا آن زمان پروتکل `http` در import `unsupported` (همان وضعیت در §۵.۴) |
| R-3 | مسیر TLS-pin failure فقط از سورس خوانده شد | تست T3 با pin اشتباه |
| R-4 | سوکت‌های warm gRPC/XHTTP پس از `core.Stop` باقی می‌مانند | فقط restart در سطح process؛ در status شمرده می‌شوند |
| R-5 | teardown سمت مقصد برای exit عمومی ~۱s است | خارج از کنترل؛ فقط سمت خودمان تضمین می‌شود |
| R-6 | DNS و policy سمت exit ناشناخته است؛ ادعا دربارهٔ hostname ارسالی است | مستندسازی؛ sniffing پایهٔ تضمین نیست |
| R-7 | `huh` sanitization ندارد | `text.SanitizeTerminal` + تست منفی T5 |
| R-8 | IPv6 end-to-end و public exit تست نشده‌اند | T3/T7 |
| R-9 | A14 (انتشار) | source offer برای MPL، بستهٔ انتشار/سورس، مجوز انتشار و بازبینی امنیت/نام؛ تصمیم D20 و ممیزی باینری انجام شده‌اند ([`licensing.md`](licensing.md)) |
| R-10 | خرابی auth خروجی VLESS/Trojan از شکست مقصد قابل تفکیک نیست؛ رکورد cooling نمی‌شود | انتخاب دستی/rerank با شاهد بایت؛ در milestone بعد شاید handshake adapter اختصاصی |
| R-11 | `ProofConfirmed` تنها assertion خروجی است؛ سرور early-reply (از جمله SOCKS خود Xray) همان reply را پیش از connect می‌دهد و کلاینت تشخیص نمی‌دهد | برچسب «exit reported success» + حذف رکورد مشکوک؛ تأیید مستقل ممکن نیست |
| R-12 | هستهٔ pin‌شده `allowInsecure` را حذف کرده؛ رکورد skip-verify همیشه `unsupported` با `insecure_tls_removed` است | تنها pin/name معتبر پذیرفته می‌شود؛ بازگشت رضایت نیازمند R-14 |
| R-13 | REALITY/ECH خارج از v1 است و پشتیبانی نمی‌شود: import آن را `unsupported`/`ReasonRealityUnverified` می‌کند (`internal/imports/record.go:178-179`) و engine با «REALITY is outside v1» رد می‌کند (`internal/engine/config.go:284-289`)؛ فیلدهای آن در مدل مانده‌اند ولی مسیر build/dial عمداً وجود ندارد | تصمیم/milestone تازه لازم است؛ کار باز v1 نیست |
| R-14 | مسیر رضایت/استثنای TLS ناامن در v1 حذف شده است | فقط با استثنای محدود، قابل‌مشاهده و قابل‌لغو که واقعاً بتواند رکورد را قابل dial کند؛ قرارداد جدید لازم است |
| R-15 | failover پیش از بایت اول CONNECT فقط ۳ exit و ۶۴KiB را پوشش می‌دهد و زمان‌دار نیست: مقصدی که عمداً ساکت می‌ماند نامتعهد می‌ماند و بایت‌های دیررس همچنان replay می‌شوند؛ مقصدی که روی همهٔ exitها پیش از بایت اول می‌بندد می‌تواند همه را cooling کند؛ replay از دید کلاینت حداکثر یک‌بار است، نه لزوماً از دید مقصد | سقف‌ها تصمیم مهندسی‌اند و با شواهد جدید بازبینی می‌شوند؛ درمان اصلی خرابی ۴۰–۵۰٪ exit، pool سالم‌تر است، نه retry نامحدود |
| R-16 | tray پیش‌فرض به StatusNotifier host وابسته است؛ GNOME بدون extension و SSH/بدون display آن را ندارند و `run` پیش‌فرض در این محیط‌ها fail می‌شود | `--headless` یا سرویس opt-in؛ fallback بی‌صدا عمداً وجود ندارد (D26) |
| R-17 | بدون سرویس نصب‌شده، crash recovery فقط best-effort و وابسته به زنده‌بودن tray است؛ قبل از login/بعد از logout هم اجرا نمی‌شود | سرویس systemd user opt-in + linger برای VPS؛ مدل دسکتاپ فقط autostart بعد از login |
| R-18 | `service install` فقط systemd user unit می‌سازد؛ system unit برای **engine**، OpenRC و سرویس‌های OSهای دیگر پشتیبانی نمی‌شوند و linger نیاز به اقدام root/polkit دارد. استثنای D29/R16 فقط `routelatch-tun.service` است: سرویس کوچک TUN با `CAP_NET_ADMIN` محدود، بدون traffic listener و فقط با `sudo routelatch tun setup` نصب می‌شود | تصمیم/پیاده‌سازی milestone بعد؛ مستند به‌عنوان مرز v1؛ این استثنا engine/tray را root نمی‌کند و بستهٔ منتشرشده همچنان هیچ unit ندارد |
| R-19 | lock فقط per data directory است؛ app/service با `--data-dir` متفاوت می‌توانند هم‌زمان اجرا شوند و بر سر port تصادم کنند. `autostart`/`service` فقط setupهای managed را از هم منع می‌کنند، نه فایل‌های دستی را | app و service پیش‌فرض همان data-dir را دارند؛ data-dir سفارشی یک instance جدا و پیشرفته است؛ تعارض port با exit 6 گزارش می‌شود |
| R-20 | پنجرهٔ migration فقط مسیرهای legacy شناخته‌شدهٔ R12 را probe/نگه می‌دارد؛ engine قدیمی default با `XDG_RUNTIME_DIR` سفارشی/غیرقابل‌حدس ممکن است دیده نشود (instanceهای `--data-dir` فقط lock همان data-dir را می‌بینند) | migration صریحاً old unit را stop/disable می‌کند؛ پس از یک نسخه سازگاری حذف می‌شود؛ `doctor` مسیر lockهای resolve‌شده را نشان می‌دهد |
| R-21 | حالت اپ برای teardown ایمن به `pidfd_open` **مجاز** نیاز دارد (Linux ≥5.3 و مسدودنشده با seccomp/EPERM)؛ در غیر این صورت `run` پیش‌فرض و `autostart enable` با `unsupported`/`7` و دلیل `pidfd_unavailable` refuse می‌کنند | `run --headless` مستقل از pidfd است؛ ارتقای kernel/رفع seccomp یا استفاده از سرویس opt-in |
| R-22 | پوشش TUN به `CAP_NET_ADMIN` (سرویس جدا) و برای bypass غیرroot به `SO_BINDTODEVICE` بدون capability (Linux ≥5.7) و systemd system manager نیاز دارد؛ در محیط‌های بدون این‌ها `tun on` با `unsupported` و دلیل امن refuse می‌شود | helper/systemd به‌عنوان پیش‌نیاز مستند؛ TUN بخشی از v1 نیست و fallback بی‌صدا یا اجرای root engine ندارد |
| R-23 | plain DNS در فاز اول مستقیم است (سیستم resolver تغییر نمی‌کند) و UDP/QUIC غیرDNS بلاک می‌شود؛ برنامه‌های UDP-only و برخی QUIC-only کار نمی‌کنند | DNS-through-pool و UDP از pool تصمیم/پیاده‌سازی جدا می‌خواهند؛ status/README این محدودیت را صریح نشان می‌دهند |
| R-24 | TUN کل ترافیک host را می‌گیرد، از جمله ترافیک کاربران دیگر؛ نصب helper با sudo و هشدار صریح است و یک TUN در هر host بیشتر مجاز نیست | مدل چندکاربره/per-UID خارج از فاز اول؛ helper فقط uid مجاز را می‌پذیرد و conflict را رد می‌کند |
| R-25 | fail-closed فقط تا زمانی معتبر است که سرویس TUN زنده باشد؛ crash/restart خود سرویس یک پنجرهٔ کوتاه fail-open دارد و یک kill-switch دائمی وجود ندارد | lease در `/run` و restart خودکار سرویس پنجره را کم می‌کند (R16-A: ~۱۱۷۰ms؛ ۲۲ms teardown + ۱۱۴۷ms systemd restart)؛ kill-switch تصمیم جدا و نیازمند firewall/privileged watchdog است |
| R-26 | دامنه برای ECH، QUIC و پروتکل‌های بدون SNI/Host قابل بازیابی نیست؛ در targeted فقط قانون IP/CIDR اعمال می‌شود. برعکس، وقتی دامنه sniff شود اولویت با domain است و IP/CIDR همان flow اعمال نمی‌شود | محدودیت sniffing در status/docs؛ راه‌حل کامل FakeIP/DNS capture برای حفظ هم‌زمان دامنه و IP تصمیم جدا می‌خواهد |
| R-27 | TUN stack پیش از اثبات اتصال مقصد به TCP پاسخ می‌دهد؛ شکست pool در TUN به‌صورت بستن/reset flow دیده می‌شود، نه refuse فوری | محدودیت ذاتی proxy در لایهٔ ۳؛ بدون direct fallback؛ محدودیت در status/docs (R16-A: engine kill → accept-then-EOF ~۱۲۰۵ms با peer echo count بدون تغییر) |
| R-28 | تغییر interface فیزیکی پیش‌فرض (Wi-Fi→Ethernet، VPN، suspend/resume) در فاز اول به‌صورت خودکار به bypass/TUN منتقل نمی‌شود؛ ممکن است تا `tun off/on` خطا یا مسیر کهنه بماند | R16-A رفتار route monitor را می‌سنجد؛ در غیر این صورت watch/restart تصمیم بعدی است و status خطا را نشان می‌دهد |
| R-29 | sidecar hysteria2 برای جلوگیری از loop زیر TUN به `route.default_interface` نسخهٔ pin‌شده وابسته است | R16-A پشتیبانی sing-box 1.14.4 و hysteria2 را runtime تأیید کرد (container، root)؛ unit hardened آزمون‌نشده است. fallback برای نسخهٔ بدون پشتیبانی/خطای runtime همچنان `unsupported`/`sidecar_loop_risk` است، نه loop |
| R-30 | هم‌زمانی TUN با VPN/TUN دیگر یا route/firewall خارجی آزمون‌نشده است و ممکن است تصادم route/blackhole بدهد (R16-A فقط routeهای هاست را read-only و LAN/VPN را شبیه‌سازی کرد؛ VPN کامل هم‌زمان آزمون‌نشده) | `tun setup` وضعیت را بررسی و conflict را روشن گزارش می‌کند؛ coexistence کامل تصمیم/آزمون جدا می‌خواهد |

**تصمیم‌های باز برای کاربر:** فقط مجوز انتشار (remote/push/tag/release) و بستن
باقی‌مانده‌های A14؛ `D20` با Apache-2.0 بسته شده و ممیزی باینری انجام شده است
([`licensing.md`](licensing.md)). هیچ تصمیم T2 بازی نمانده است؛ R-12/R-14 محدودیت
ادعا و شرط بازگشت آینده‌اند، نه تصمیم ناتمام.

## ۱۰. پوشش TUN (D29؛ قرارداد R16 — مدل داده/opهای R16-B، سرویس R16-C، engine R16-D و CLI R16-E پیاده‌شده؛ نصب واقعی/tray/packaging باز)

این بخش قرارداد milestone پس از v1 است. R16-B (تایپ‌های `internal/contract/tun.go`،
فیلدهای config/status، opهای management و seam تزریق‌شدنی)، سرویس جداگانهٔ R16-C
(`internal/tun` و `__tun-service`)، اتصال engine در R16-D (`start/stop`، ماندگاری،
adoption و bypass ترافیک خود RouteLatch) و سطح CLI در R16-E (`tun status|on|off`
و `tun setup|uninstall` privileged) پیاده شده‌اند. نصب واقعی/readiness/rollback
helper در R16-G، tray در R16-F و بسته‌بندی/مستندات کاربر در R16-G باز است؛ منطق
setup در E فقط CLI-only و unit-testable است و هیچ نصب میزبان انجام نمی‌دهد.
production runtime را از طریق کلاینت `internal/tun` به control وصل می‌کند؛ نبود
socket/helper فقط warning و `off` می‌دهد و listener عادی را از کار نمی‌اندازد.
نام‌ها و شکل‌های زیر برای بخش پیاده‌شده pin شده‌اند و برای R16-D به بعد الزام‌آورند.

### ۱۰.۱ مدل و مرز

- TUN یک لایهٔ پوشش روی modeهای موجود است، نه یک `Mode` تازه. `Mode` همچنان
  `targeted|all-proxy|all-direct|stopped` است و هیچ مقدار TUN به آن اضافه نمی‌شود.
- `tun enable` فقط وقتی مجاز است که mode غیر `stopped` باشد. `tun disable` همیشه
  مجاز است. `mode stopped`، Quit و `tun uninstall` **اول** TUN را می‌بندند و بعد
  listenerها/process را؛ شکست بستن TUN باید توقف را متوقف کند (fail-closed).
- `mode stopped` مقدار `tun_enabled` را پاک نمی‌کند ولی TUN را می‌بندد و تا
  انتخاب یک mode غیر stopped اجازهٔ فعال‌شدن نمی‌دهد؛ Quit هم همین preference
  را نگه می‌دارد. فقط `tun off` (یا uninstall) مقدار را false می‌کند تا
  «خاموش‌کردن صریح پوشش» از «توقف موقت» جدا باشد. اگر process جدید در mode
  `stopped` بالا بیاید و TUN فعالی از crash باقی مانده باشد، قبل از اعلام
  readiness آن را disable می‌کند؛ شکست disable خطای status/doctor است، نه
  بازکردن listener روی mode stopped.
- `tun off` در هر mode (از جمله `stopped`) خواست `tun_enabled=false` را ذخیره
  می‌کند، حتی وقتی helper نصب نیست یا سرویس پاسخ نمی‌دهد. فقط گزارش مثبت
  `off` از status، helper اثباتاً غایب (missing)، یا `Disable` موفق (nil) اجازه
  می‌دهد bypass موتور آزاد شود و پاسخ `off` باشد؛ Disable موفق حتی با probe
  ناشنوا/کهنه معتبر است. اگر helper ناشنوا/refused/hung یا `unavailable`
  باشد و disable هم موفق نشود، op موفق و idempotent است (نه `internal`) ولی
  off اعلام نمی‌شود: bypass فعال می‌ماند، coverage «uncertain» علامت می‌خورد و
  status `unavailable` با `tun_conflict` می‌دهد (§۱۰.۲، §۱۰.۶). شکست disable
  روی پوششی که status آن را `on|starting|stopping|error` گزارش کرده
  `internal` می‌ماند (با حفظ bypass)؛ در آن حالت هم خواست پاک می‌شود تا status
  تعارض «پوشش روشن بدون درخواست» را نشان دهد. شکست ذخیرهٔ config هم طبق
  §۱۰.۳ `internal` است.
- ترافیک TCP پذیرفته‌شده در TUN به SOCKS5 لوپ‌بک همان instance تحویل می‌شود و
  از همان `Router`/registry/`PolicyVersion` عبور می‌کند. سرویس TUN هیچ rule،
  pool، exit selection یا تصمیم مقصدی ندارد.
- دامنه برای TLS/HTTP با sniffing بازیابی می‌شود؛ وقتی دامنه موجود باشد همان
  هویت مقصد است و قانون IP/CIDR برای آن flow اضافه اعمال نمی‌شود. برای
  ECH/QUIC/پروتکل بدون metadata فقط IP/CIDR معنا دارد. sniffing فقط metadata
  flow را در حافظه دارد.

### ۱۰.۲ config، state و status

- `Config` یک فیلد additive و پیش‌فرض `false` به نام `tun_enabled` می‌گیرد؛
  schema version فعلی (۱) تغییر نمی‌کند چون فایل قدیمی با مقدار صفر معتبر است.
  این فیلد «خواست کاربر» است، نه اثبات فعال‌بودن TUN. در JSON کانفیگ همیشه
  نوشته می‌شود (مثل بقیهٔ فیلدهای scalar، بدون `omitempty`)، و فایل قدیمی بدون
  کلید با `false` decode می‌شود.
- `state.json` هیچ مقصد، نام دامنه، آدرس TUN یا تاریخچهٔ flow نمی‌گیرد (D21)؛
  `tun_enabled` و وضعیت runtime فقط در config و status می‌مانند.
- `status` یک شیء additive به نام `tun` می‌دهد:
  `desired` (bool)، `state` (`off|starting|on|stopping|error|unavailable`)،
  `interface` (مثلاً `rl-tun0`)، `since`، `dns` (`direct`)، `udp` (`blocked`)،
  `ipv6` (bool)، `helper` (`installed|missing|incompatible`)،
  `last_error` (کد/دلیل امن، بدون متن آزاد حاوی مقصد) و `detail` کوتاه.
  target SOCKSِ سرویس در status عمومی منتشر نمی‌شود؛ R16-D آن را فقط روی socket
  خصوصی سرویس می‌خواند تا target کهنه را تشخیص دهد (بدون مقصد/دامنه).
- `dns`/`udp` مقادیر ثابت فاز اول‌اند، نه انتخاب runtime: `dns` همیشه `direct`
  و `udp` همیشه `blocked` است (§۱۰.۵، D30). هر مقدار دیگر runtime به همین دو
  مقدار pin می‌شود تا نشانی resolver یا متن آزاد از این فیلدها بیرون بماند.
- `last_error` فقط از دلیل‌های خطای TUN (`tun_helper_unavailable`،
  `tun_platform_unsupported`، `tun_conflict`، `tun_service_unavailable`،
  `sidecar_loop_risk`) است؛ `sidecar_loop_risk` فقط وقتی گزارش می‌شود که
  sidecar نتواند interface فیزیکی را bind کند و پوشش عمداً روشن نشده است.
  شناسه‌های warning واژگان جدا دارند. هر مقدار دیگر runtime حذف می‌شود.
  `last_error`/`detail` وقتی خالی‌اند حذف می‌شوند و `detail` متن ثابت امن است؛
  `interface`/`detail` از control-character پاک می‌شوند.
- `since` زمان شروع runtime است. وقتی runtime زمان شروع را نمی‌داند یا هنوز
  اجرا نشده، کلید حذف می‌شود؛ زمان صفر ساختگی سریالایز نمی‌شود.
- `helper=missing` یعنی «helper نصب‌شده و تأییدشدهٔ قابل استفاده در دسترس نیست»،
  نه اثبات نبودن فایل یا باینری. وقتی `tun.status` از runtime شکست بخورد، همین
  مقدار محافظه‌کارانه با `last_error=tun_service_unavailable` گزارش می‌شود و
  warning `tun_helper_missing` صادر نمی‌شود، چون آن وضعیت با
  `tun_service_unavailable` بیان شده است.
- status فعلی همیشه شیء `tun` را می‌فرستد؛ مصرف‌کنندهٔ payload قدیمی بدون کلید آن
  را nil/unknown می‌بیند، نه `off`. مثال‌های §۴.۲/§۷.۱ همین شکل additive را
  نشان می‌دهند.
- `status.ready` همچنان فقط management/listener را می‌سنجد؛ `tun.desired=true`
  با `tun.state!=on` یک warning و نشانگر tray است، اما proxy عادی را از کار
  نمی‌اندازد. `run`/`service` فقط بعد از listener readiness برمی‌گردند. helper
  غایب/ناشنوا بدون پوشش فعال اثبات‌شده فقط warning و `off`/`unavailable` می‌دهد.
  اگر پوشش جامانده نتواند اثباتاً خاموش شود، egress bypass فعال می‌ماند و
  listenerها با خطای status/doctor باز می‌شوند؛ فقط وقتی bypass قابل برقراری
  نیست (احتمال loop) یا mode برابر stopped است و disable شکست می‌خورد، آمادگی
  قبل از listenerها متوقف می‌شود (§۱۰.۱، §۱۰.۶).
- `warnings` نسخهٔ TUN شامل این شناسه‌های ثابت است: `tun_dns_direct`،
  `tun_udp_blocked`، `tun_helper_missing`، `tun_service_unavailable`،
  `tun_domain_unavailable` (در صورت نیاز)، `tun_desktop_proxy_overlap`،
  `tun_all_direct` و `tun_conflict`. هر warning فقط وقتی صادر می‌شود که برای
  وضعیت گزارش‌شده صادق باشد (مثلاً `tun_dns_direct`/`tun_udp_blocked` فقط با
  `state=on` و در mode متوقف هیچ‌کدام).

### ۱۰.۳ opها و فرمان‌ها

| op | ورودی | نتیجه | خطاهای مجاز |
| --- | --- | --- | --- |
| `tun.status` | — | `StatusData.tun` | `internal` |
| `tun.enable` | — | `tun.state=starting\|on` | `stopped`, `unsupported`, `state_locked`, `internal` |
| `tun.disable` | — | `tun.state=off`؛ یا `unavailable`/`tun_conflict` وقتی helper ناشنوا/`unavailable` است و disable موفق نمی‌شود | `internal` |

- `tun.enable` ورودی دلخواه (interface/route/domain) ندارد؛ listener هدف از
  config همان instance گرفته می‌شود و سرویس TUN فقط `127.0.0.1:port` یا
  `[::1]:port` را می‌پذیرد.
- tray (R16-F) فقط همین دو op را از زیرمنوی Coverage با تأیید دو مرحله‌ای صدا
  می‌زند؛ enable با mode stopped/helper ناموجود یا ناسازگار/state فعال یا
  درحال‌گذار disabled است و disable (کاهش پوشش) در هر mode از جمله stopped
  مجاز می‌ماند. tray هیچ setup/uninstall یا route/interface دلخواهی ندارد و
  دلیل خطای نمایش‌داده‌شده فقط از واژگان امن §۱۰.۲ است.
- `routelatch tun status|on|off` معادل همین opهاست. `routelatch tun setup` و
  `routelatch tun uninstall` **privileged CLI-only** هستند، از management socket
  عبور نمی‌کنند، در tray آیتم مستقیم ندارند و در `Terminal menu` فقط راهنما/دستور
  نشان داده می‌شود (D28).
- اگر آدرس SOCKS فعال با config جدید فرق کند (تغییر listener + restart)، engine
  اول TUN را disable می‌کند و سپس enable را با همان کانفیگ سرویس امتحان می‌کند؛
  آدرس هدف فقط در `/etc/routelatch/tun.json` (root-owned) است و R16-D آن را
  بازنویسی نمی‌کند. اگر هدف هنوز کهنه باشد، engine دوباره disable می‌کند و
  `state_locked`/`tun_conflict` برمی‌گرداند تا کاربر `tun setup` (R16-G) را با
  آدرس جدید اجرا کند؛ سرویس target flowهای زنده را بی‌صدا عوض نمی‌کند. بودجهٔ
  درخواست management و shutdown (`run`/supervisor) بزرگ‌تر از عملیات ۳۰ثانیه‌ای
  سرویس به‌علاوهٔ re-sync جانبی است تا disable کند اما موفق به timeout تبدیل نشود.
- `tun setup` با sudo اجرا می‌شود؛ idempotent است؛ unit/config مالک خود را
  می‌شناسد و فایل غیرمالک را بدون `--force` بازنویسی نمی‌کند. `tun uninstall`
  فقط unit/config/lease مالک را حذف می‌کند و اگر TUN فعال است اول disable می‌کند.
  `tun setup --socks HOST:PORT` فقط listener لوپ‌بک engine را می‌پذیرد (پیش‌فرض
  `127.0.0.1:8318`) و `--force` تنها برای unit/config غیرمالکِ غیرفعال است؛
  setup هرگز یک unit فعال غیرمالک یا binary غیرمالک را جایگزین نمی‌کند.
- codeهای خروج از همان جدول §۷.۲ می‌آیند؛ ErrorKind تازه ساخته نمی‌شود.
  نبود helper/kernel/systemd → `unsupported`/`7` با دلیل پایدار
  `tun_helper_unavailable` یا `tun_platform_unsupported`؛ تعارض instance →
  `state_locked`/`6` با دلیل `tun_conflict`؛ mode stopped → `stopped`/`3`.
- دلیل امن در `error.detail.reason` می‌آید و پیام خطا برای هر دلیل متن ثابت است،
  نه متن آزاد runtime. دلیلی که در allowlist نیست حذف می‌شود و `unsupported`/
  `state_locked` بدون دلیل پایدار به `internal` تبدیل می‌شود. `tun.disable`
  همیشه مجاز است و خطاهای مجازش فقط `internal`اند؛ هر خروجی دیگری از runtime در
  disable (از جمله تعارض یا unsupported) به `internal` نگاشت می‌شود تا جدول بالا
  نشکند. شکست ذخیرهٔ `tun_enabled` (config store) هم برای هر دو op
  `internal`/`1` است، نه kind ذخیره‌سازی.
- `tun.disable` در همهٔ حالت‌ها خواست `tun_enabled=false` را پاک می‌کند. نبود
  helper یا runtime ناشنوا/`unavailable` op را موفق/idempotent می‌کند (نه
  `internal`)، ولی bypass موتور فقط با `state=off` اثبات‌شده یا helper غایب
  آزاد می‌شود؛ در حالت ناشنوا/`unavailable` bypass می‌ماند، coverage
  uncertain علامت می‌خورد و status `unavailable`/`tun_conflict` می‌دهد. شکست
  disable روی پوششی که status آن را فعال/در حال گذار گزارش کرده `internal`
  می‌ماند (با حفظ bypass) و `off` اعلام نمی‌شود. پس از آن، `desired=false` با
  `state=on` در status به‌صورت `tun_conflict` دیده می‌شود (§۱۰.۲). `tun.disable`
  موفق (nil) همیشه `state=off` برمی‌گرداند، حتی اگر probe بعدی ناشنوا یا کهنه
  باشد؛ `unavailable`/`tun_conflict` فقط نتیجهٔ تلاش ناموفق disable است. شکست
  ذخیرهٔ خواست در همان تلاش ناموفق، `internal` پایداری runtime را حفظ می‌کند و
  snapshot قبلی (desired=true) را برای status نگه می‌دارد.
- در R16-B که runtime واقعی وجود ندارد: `tun.status` مقدار `off` (یا با
  `tun_enabled=true` مقدار `unavailable`) می‌دهد، `tun.enable` با
  `unsupported`/`tun_helper_unavailable` رد می‌شود و خواست کاربر را ذخیره
  می‌کند، و `tun.disable` خواست را پاک می‌کند و `off` برمی‌گرداند.
  `mode stopped` پیش از commit/listener بستن، disable را صدا می‌زند و شکست آن
  توقف را متوقف می‌کند؛ فقط گزارش صریح `off` از status باعث صرف‌نظر از disable
  می‌شود و `unavailable`/خطای status هم disable را می‌طلبند (fail-closed).
  recovery TUN جامانده در startup و Quit بخش R16-D است.
  `restore` بکاپی که mode آن stopped است هنوز مسیر disable را صدا نمی‌زند؛ این
  مورد با lifecycle کامل R16-C/D بسته می‌شود و در R16-B تعمداً تغییر نکرده است.

### ۱۰.۴ قرارداد helper و unit (پیشنهادی)

- `tun setup` باید uid کاربر فراخوان (`SUDO_UID`/real uid) را ثبت کند، نه uid
  روت؛ اجرای مستقیم با root بدون کاربر هدف مجاز نیست. این فایل‌های root-owned
  را می‌سازد (و در uninstall پاک می‌کند):
  - `/etc/systemd/system/routelatch-tun.service`
  - `/etc/systemd/system/routelatch-tun.socket`
  - `/etc/routelatch/tun.json` (فقط `allowed_uid` و `socks_address`؛ مسیر
    باینری از unit root-owned می‌آید و نام/mtu/gateway/routeها در کد سرویس
    ثابت‌اند؛ بدون secret و بدون input کاربر)
  - `/run/routelatch-tun/` (lease و socket؛ `/run` با boot پاک می‌شود)
- unit با `CapabilityBoundingSet=CAP_NET_ADMIN`, `AmbientCapabilities=CAP_NET_ADMIN`,
  `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`,
  `PrivateTmp=yes` و `Restart=on-failure` اجرا می‌شود. `User` همان uid مجاز است؛
  engine/tray هرگز root نمی‌شوند. سرویس در شروع euid را می‌سنجد: اجرا با root
  یا uid غیر از `allowed_uid` رد می‌شود، حتی اگر socket را systemd با مالک uid
  به آن داده باشد.
- **باینری سرویس must root-owned باشد:** setup یک کپی از باینری را در مسیر
  root-owned مثل `/usr/libexec/routelatch/tun-service` (0755) می‌گذارد و unit
  همان را اجرا می‌کند؛ هرگز باینری قابل‌نوشتن کاربر (مثل `$HOME/.local/bin`) را
  با `CAP_NET_ADMIN` اجرا نمی‌کند. کپی باید با version/commit همان binary باشد و
  upgrade دوباره `tun setup` می‌خواهد. `RuntimeDirectory=routelatch-tun` برای
  lease/socket با مالکیت uid ساخته می‌شود و `RuntimeDirectoryPreserve=yes`
  lease را در restart سرویس نگه می‌دارد (در boot تازه `/run` پاک است).
- `PrivateDevices=yes` نباید `/dev/net/tun` را پنهان کند؛ یا خاموش می‌ماند یا
  `DeviceAllow=/dev/net/tun rw`/bind معادل دارد.
- شکل pin‌شدهٔ `/etc/routelatch/tun.json` (R16-C/G) فقط دو فیلد دارد:
  `allowed_uid` (عدد نامنفی؛ uid مجاز engine) و `socks_address` (فقط IP literal
  لوپ‌بک `127.0.0.0/8` یا `::1` با پورت معتبر). نام/MTU/gateway/routeها، مسیر
  socket و lease در سرویس ثابت‌اند (`rl-tun0`، `1500`، `198.18.0.1/15`،
  `fc00::1/64`، `/run/routelatch-tun/{control.sock,lease.json}`)؛ فیلد ناشناخته
  config رد می‌شود و هیچ interface/route/domain/log-level از آن پذیرفته نیست.
- control socket `0600`، peer credential برابر uid مجاز، ورودی فقط listener
  لوپ‌بک و `enable|disable|status`. هر درخواست دیگر/آدرس غیرلوپ‌بک رد می‌شود.
  `.socket` unit با `SocketMode=0600`/`SocketUser=<uid>` ساخته می‌شود (مسیر
  `/run/routelatch-tun/` را setup با مالکیت uid می‌سازد)؛ service در disable با
  exit 0 خارج می‌شود و socket برای enable بعدی باقی می‌ماند.
- lease `/run/routelatch-tun/lease.json` فقط برای بازسازی fail-closed پس از
  restart سرویس در همان boot است و `boot_id` دارد؛ mismatch/discard. در boot
  تازه TUN خودکار ساخته نمی‌شود؛ engine بر اساس `tun_enabled` آن را درخواست
  می‌کند.
- یک TUN در هر host: نام `rl-tun0` و lock سراسری؛ instance دوم/`--data-dir`
  دوم با `state_locked`/`tun_conflict` رد می‌شود. unit نصب‌شده بخشی از بسته
  نیست (D26).

### ۱۰.۵ کانفیگ سرویس TUN (ثابت، تولیدشده)

- interface `rl-tun0`، MTU 1500، IPv4 gateway غیرقابل‌مسیریابی
  `198.18.0.1/15` و IPv6 ULA `fc00::1/64` (در R16-A تأیید شدند: بدون تصادم با
  routeهای واقعی هاست به‌صورت read-only و LAN/VPN شبیه‌سازی‌شده؛ coexistence
  واقعی VPN کامل همچنان R-30 است. `198.18.0.0/15` محدودهٔ benchmarking است و
  setup باید conflict را روشن گزارش کند).
- routeها: `0.0.0.0/1`، `128.0.0.0/1`، `::/1`، `8000::/1` با metric 1؛
  default route سیستم جایگزین نمی‌شود تا routeهای LAN/DNS محلی حفظ شوند.
- Xray TUN inbound با sniffing `http,tls`، `routeOnly=false`، `logLevel:
  "none"` و `access: "none"` **ثابت**؛ هیچ knob یا فایل لاگ سطح‌دار وجود ندارد
  (D21؛ stock Xray در info/debug مقصد را در `proxy/tun/handler.go:246` لاگ
  می‌کند؛ هر debug آینده نیازمند patch/redaction و تصمیم جدید است. R16-A این را
  با canary در تنظیمات shipped تأیید کرد). هیچ inbounds دیگر/API/routing کاربری
  در این core نیست.
- routing داخل سرویس TUN:
  1. TCP همه → outbound `rl-tun-socks` به SOCKS لوپ‌بک engine.
  2. UDP/TCP پورت 53 → outbound `rl-tun-dns` از نوع freedom، با
     `autoOutboundsInterface=auto` به interface فیزیکی (DNS مستقیم، بدون proxy؛
     این معادل در R16-A آزموده شد و peer منبع را eth0 دید؛ bind صریح
     `sockopt.interface` در سرویس غیرroot timeout داد و استفاده نمی‌شود).
  3. UDP غیر 53 → `rl-block` (blackhole).
  4. final → `rl-block`.
- `autoOutboundsInterface=auto` (یا معادل تست‌شده) تا dialهای خود سرویس TUN
  وارد TUN نشوند؛ DNS مستقیم و loopback SOCKS مستثنا هستند. `autoSystemDnsToGateway`
  هرگز روشن نمی‌شود. firewall/NAT/ip_forward/DNS سیستم تغییر نمی‌کند.
- سرویس با `core` تازه در هر enable شروع می‌شود و با disable خارج می‌شود؛
  restart درون‌process ممنوع است (R-4). یک enable شکست‌خورده (core.Start،
  CheckUp یا نوشتن lease) core/link خود را پاک می‌کند و core شمرده نمی‌شود،
  پس retry یک core تازه می‌سازد؛ اگر پاک‌سازی کامل نشود، restart لازم است.
  Stop باید route/address/link را پاک کند و خطای پاک‌سازی را گزارش دهد.

### ۱۰.۶ bypass ترافیک خود RouteLatch

- در حالت TUN، همهٔ dialهای غیرلوپ‌بک engine (direct، probe، fetch اشتراک،
  resolver سفارشی) به interface فیزیکی پیش‌فرض bind می‌شوند؛
  `SO_BINDTODEVICE` از Linux 5.7 برای کاربر غیرroot مجاز است. loopback مستثناست.
- dialهای pool داخل core با یک controller ثبت‌شده در start که فقط در حالت TUN
  bind می‌کند bypass می‌شوند؛ این registration یک‌بار در هر process است و نباید
  بعد از disable روی dialهای عادی اثر بگذارد (R16-D: یک controller مشترک با
  flag فعال/غیرفعال؛ interface فیزیکی از default route به‌جز `rl-tun0` و loopback
  کشف می‌شود و kernel < 5.7 پیش از enable `unsupported` می‌دهد).
- sidecar sing-box در حالت TUN `route.default_interface` (یا
  `auto_detect_interface`) به interface فیزیکی می‌گیرد؛ این مقدار از کانفیگ
  تولیدی ما می‌آید و از import پذیرفته نمی‌شود. اگر نسخهٔ pin‌شده این فیلد را
  پشتیبانی نکند، exitهای hysteria2 در حالت TUN با `unsupported`/دلیل
  `sidecar_loop_risk` کنار گذاشته می‌شوند، نه اینکه loop کنند.
- اگر kernel < 5.7 یا bind شکست بخورد، TUN باید fail کند (`unsupported`)، نه
  اینکه loop/blackhole یا direct نشت کند.

### ۱۰.۷ حریم خصوصی، امنیت و محدودیت‌ها

- D21: هیچ مقصد/دامنه/flow در log، state، backup، notification، label tray یا
  helper log نوشته نمی‌شود. sniffing metadata فقط در حافظهٔ همان flow است.
- TUN تمام ترافیک host را می‌گیرد، از جمله کاربران دیگر؛ setup با sudo و هشدار
  صریح است. یک TUN در هر host؛ مدل per-UID/per-app خارج از فاز اول (R-24).
- plain DNS مستقیم است و «proxied» ادعا نمی‌شود؛ UDP/QUIC غیرDNS بلاک است
  (R-23). دامنه برای ECH/QUIC/غیرTLS در دسترس نیست (R-26).
- fail-closed تا زنده‌بودن سرویس TUN معتبر است؛ crash/restart سرویس یک پنجرهٔ
  fail-open کوتاه دارد و kill-switch دائمی خارج از فاز اول است (R-25).
- TUN stack ممکن است TCP را پیش از اثبات handshake مقصد accept کند؛ در این حالت
  خطای pool به‌صورت بستن/reset همان flow دیده می‌شود، نه refuse فوری، ولی هیچ
  fallback مستقیمی رخ نمی‌دهد. این تفاوت باید در status/docs باشد (R-27).
- TUN می‌تواند با پروکسی دسکتاپ هم‌زمان باشد؛ برنامه‌هایی که از system proxy
  استفاده می‌کنند به listener می‌روند، نه TUN. RouteLatch تنظیم دسکتاپ را خودکار
  عوض نمی‌کند؛ warning `tun_desktop_proxy_overlap`. در `all-direct` هم TUN مجاز
  است ولی سودی برای pool ندارد و UDP غیرDNS را همچنان بلاک می‌کند؛ warning
  `tun_all_direct`. هم‌زمانی با VPN/TUN دیگر آزمون‌نشده و ممکن است route conflict
  بدهد (R-30)؛ setup باید آن را روشن گزارش کند، نه بی‌صدا.
- هیچ MITM/root CA/کاهش TLS، هیچ تغییر DNS سیستم، هیچ LAN listener/forwarding،
  و هیچ rule/pool مستقل در سرویس TUN مجاز نیست.
- اگر engine در دسترس نیست و باید TUN فوراً برداشته شود، مسیر recovery
  `sudo routelatch tun uninstall` (یا معادل مستند `systemctl stop
  routelatch-tun.service`) است؛ `tun off` عادی از management socket استفاده
  می‌کند و در نبود engine کافی نیست. این مسیر باید در README/doctor بیاید.

### ۱۰.۸ نگاشت پذیرش

| معیار | check حداقلی |
| --- | --- |
| A17 | helper setup/units/permissions/idempotency/uninstall؛ `tun on/off/status` و JSON/exit؛ dual-stack route/link؛ TCP به SOCKS و domain sniff TLS/HTTP؛ IP/CIDR و targeted/all-proxy/all-direct؛ localhost/LAN direct؛ DNS مستقیم و UDP بلاک؛ stop/Quit ordering؛ قطع engine و fail-closed؛ restart/adopt؛ ماندگاری و helper غایب؛ tray menu/icon/colors؛ صفر مقصد در log/state |
| A02/A03/A04 | همان ماتریس §۲.۵ روی flowهای TUN؛ pool empty/dead → خطا، نه direct؛ mode switch → registry sweep |
| A08/A09/A12 | DNS سیستم بدون تغییر؛ hostname تا exit؛ loopback-only؛ socket/lease `0600`؛ UDP غیرDNS `0x07`/blackhole بدون سوکت مستقیم |
| A15 | `run` readiness و tray؛ stop/restart/Quit با TUN؛ crash/teardown بدون orphan TUN |

DNS-through-pool و پوشش UDP در D30/R16-I/J تعریف شده‌اند و **بخشی از این
قرارداد فاز اول نیستند**؛ DNS یک switch مستقل خواهد داشت (جدا از mode) و تا
verdict آن‌ها، DNS مستقیم/UDP بلاک می‌ماند. هر پیاده‌سازی نیازمند بخش/acceptance
جدید در همین سند است، نه تغییر خاموش A17.

**قدم بعد:** R16-B/C/D (مدل داده، سرویس، اتصال engine و bypass) و R16-E (CLI،
layout/معنای setup و uninstall با fake paths/systemctl) پیاده و تست شده‌اند، ولی
این پوشش هنوز فعال یا نصب‌شده نیست: tray (F)، نصب واقعی/readiness/rollback،
بسته‌بندی و مستندات کاربر (G) و پذیرش A17 (H) باز است. R16-A فقط پس از مجوز
صریح؛ ادعای پشتیبانی TUN در v1.1.0 وجود ندارد.
