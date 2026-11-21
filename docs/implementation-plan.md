# پلن پیاده‌سازی RouteLatch

نسخهٔ اولیهٔ پلن — ۲۰۲۶-۱۱-۱۷

**داشتن پلن به‌تنهایی مجوز پیاده‌سازی، نصب سرویس یا انتشار نیست.** محدودهٔ مجاز
هر جلسه از درخواست جاری کاربر و بخش [کارهای باز](#open-work) مشخص می‌شود.

## نتیجه و مرز

نسخهٔ اول مطابق D01–D20 در [نیازمندی‌ها](requirements.md): بستهٔ خودکفای Linux،
Go/Xray، پروکسی TCP هدفمند، مدیریت pool و پیشنهادها، CLI/منوی ساده، استفادهٔ
اختیاری پروکسی دسکتاپ. Windows، Android، TUN، UDP و GUI milestones بعدی‌اند.

نقطهٔ شروع پلن، پروژه‌ای بدون کد، executable، dependency lock یا framework آزمون است. مسیرهای
کد در این سند **پیشنهادی** هستند؛ structure نهایی بعد از T1/T2 انتخاب می‌شود.
مدارک خارجی و سورس قدیمی در [تحقیق](research.md) با حدود پوشش آمده‌اند.

## T1 — اثبات مرز frontend و هستهٔ Xray

- **نتیجه:** نشان دهیم D05/D11/D13 و پیش‌نیاز تشخیص خطا برای A05 با API واقعی نسخهٔ pinned قابل انجام‌اند؛
  قبل از این نتیجه، ساخت اصلی adapter آماده نیست.
- **دامنهٔ پیشنهادی:** یک check محدود در مسیر اختصاصی پروژه، نه سرویس نصب‌شده؛
  ابزار Go/Xray و کتابخانه‌های نامزد HTTP/SOCKS. هیچ چارچوب آزمون جدید بی‌تصمیم
  تحمیل نشود؛ پس از انتخاب نسخهٔ Go/toolchain، روش آزمون متناسب با پروژه تثبیت شود.
- **وابستگی:** مجوز شروع اجرای check از کاربر؛ دسترسی به toolchain و دانلودهای
  عمومیِ لازم. نمونهٔ خصوصی/شبکه‌ای نیاز به مجوز جدا دارد.
- **کار:** نسخهٔ Xray/toolchain را بررسی و pin؛ تفاوت core.Dial-return و اتصال
  واقعی، tracked error، cancel، first-byte، CONNECT/SOCKS replies و teardown را
  با peerهای محلی کنترل‌شده بررسی کن. مسیر direct از pool مستقل باشد.
- **کار:** candidate frontend را برای حفظ hostname، no local DNS قبل از pool dial،
  stream/body/status/redirect، no MITM و no non-idempotent replay بسنج.
- **کار:** روش probe با core فعال یا helper ایزوله را بررسی کن؛ از ایجاد چند core
  هم‌زمان در process با وضعیت global بدون شاهد خودداری کن. parsing reuse مانند
  libXray باید preservation، sanitization و گزارش invalid/quarantine را پشتیبانی کند.
- **آزمون مورد انتظار:** success واقعی، refused، DNS failure، timeout، reset پس از
  write، EOF عادی و cancel از تغییر حالت متمایز باشند؛ هیچ درخواست اصلی تکرار نشود؛
  feedback به مقصد و session درست برسد؛ زمان teardown اندازه‌گیری و باقی‌ماندن
  socket/goroutine بررسی شود؛ حد پذیرش عددی در T2 از این شواهد تعیین شود.
- **دروازهٔ handshake:** مشخص کن frontend چه زمانی HTTP CONNECT `2xx` یا SOCKS5
  `0x00` می‌فرستد. برگشت `core.Dial` کافی نیست. پذیرش نیازمند شاهد برقراری واقعی
  اتصال مقصد، یا راه جایگزینِ مستند و آزموده است که با قرارداد پروتکل و D11 سازگار
  باشد؛ راه جایگزینی که تضمین را تغییر دهد نیاز به تأیید کاربر دارد. refusal/timeout
  را پیش و پس از reply، از جمله transport دارای ارسال اولیه/early data، بررسی کن.
  نبود connected event یا readiness قابل اثبات نباید با TCP زنده به سرور واسط یا
  first-byte حدسی جایگزین شود؛ در این صورت D05 و مرز handshake باز می‌شوند.
- **آزمون مورد انتظار:** اتصال direct زنده و اتصال pool زنده را ثبت کن؛ mode switch
  اتصال ناسازگار و in-progress را ببندد و سازگار را حفظ کند؛ pool خالی/خطای tag به
  direct سقوط نکند. نمونه‌های TCP/TLS/WS/gRPC/XHTTP لازم بر اساس امکانات نسخه،
  با منابع کنترل‌شده اجرا شوند؛ هیچ credential عمومی/خصوصی در fixture نگذار.
- **آزمون مورد انتظار:** idle keep-alive pool و کانال hijacked CONNECT را هم ثبت
  کن؛ بعد از تغییر mode/rule هیچ اتصال pooled قبلی درخواست جدید را با مسیر متعارض
  حمل نکند. بستن یا drain/revalidation باید با counter/مشاهدهٔ peer اثبات شود؛
  بی‌کاربودن اتصال در لحظهٔ تغییر مجوز reuse مسیر قبلی نیست.
- **ثبت:** commands، نسخه/commit، خروجی، محدودهٔ مشاهده و transportهای آزموده‌نشده؛
  نتیجهٔ check جای ادعای امنیت کامل نیست.
- **دروازه:** اگر نتیجه ناموفق/مبهم شد، D05 و راه حل وابسته را با کاربر باز کن؛
  log heuristic، restart همهٔ اتصال‌ها یا کاهش تضمین را پنهانی جایگزین نکن.

## T2 — بستن قراردادهای فنی و امنیتی

- **نتیجه:** قرارداد data/control/pool و معیار آزمون عددی، بدون تصمیم پنهان.
- **وابستگی:** نتیجهٔ T1 و تصمیم کاربر برای شروع ساخت.
- **دامنهٔ پیشنهادی:** به‌روزرسانی `docs/architecture.md` و همین پلن؛ سپس config
  schema و ساختار `cmd/` و `internal/` در صورت شروع implementation.
- **کار:** فهرست مقصد مشترک، normalization، اولویت استثنای local، IP literal،
  حوزهٔ rule زیردامنه و رفتار hostname→IP را دقیق کن؛ resolver/CDN را مرز ادعا بدان.
- **کار:** مدیریت خصوصی Linux (socket permissions/peer checks)؛ config/state schema
  version، یک writer و singleton، atomic mutation/recovery؛ Go public libs را انتخاب
  و pin کن. persistent store/file format بر اساس mutation واقعی، نه حدس انتخاب شود.
- **کار:** formatهای ورودی subscription، export/backup، invalid node policy و مالکیت
  رکورد بین منابع؛ v2rayN schema را از نمونهٔ مجاز یا کد/مستند فعلی کشف کن.
- **کار:** سیاست quarantine برای insecure link (بدون مسیر رضایت در نسخهٔ اول؛ D18) و
  پذیرش pin گواهی با verification روشن؛ schema و sanitization fields، TLS/raw HTTP
  warnings و no root config execution.
- **کار:** نوع probe برای hostname/IP، triggerهای مجاز، cooldown/retention/limits؛
  رفتار background management در all-direct/stopped و bootstrap refresh هنگام pool
  خراب را صریح کن. توقف به معنی عدم شبکه/worker فعال، مگر عملیات صریح CLI مجاز باشد.
- **کار:** آستانه‌های سلامت/رتبه‌بندی را با check کنترل‌شده و دادهٔ مجاز تعیین کن؛
  retry deadline/backoff و stable selection، سرعت در برابر throughput و شکست را تفکیک کن.
- **کار:** روش انتقال اتمیک mode/rules و handling late errors/pending dials؛ public
  error model بدون چاپ secrets. فرمان‌های CLI/JSON خروجی و معادل منو را تثبیت کن.
- **کار:** حداقل Linux/arch و محیط دسکتاپ پشتیبانی‌شده را با کاربر انتخاب کن؛ restore
  و offline recovery، عدم دست‌کاری کاربر، service ownership و سیاست port collision.
- **دروازه:** گزینه‌های meaningful دربارهٔ scope/storage/privacy که باقی‌اند با
  کاربر حل شوند؛ مسیر کد و commands پس از آن actual شوند، نه از پیش اختراع‌شده.
- **آزمون مورد انتظار:** جدول مسیر/حالت و خطا برای ورودی‌های مرزی نوشته شود و
  هر A01–A14 به task و acceptance وصل باشد؛ review طرح تناقض نداشته باشد.

## T3 — مسیر TCP و مدیریت اتصال

- **نتیجه:** HTTP/CONNECT و SOCKS5 مطابق A02–A04/A08/A09/A12؛ direct مستقل.
- **وابستگی:** T1/T2.
- **دامنهٔ پیشنهادی:** `internal/proxy/`, `internal/routing/`, `internal/engine/`,
  `internal/connections/`, config/state و تست‌های همان حوزه؛ تقسیم پوشه تصمیم فنی است.
- **کار:** frontendهای آماده، adapter pinned Xray، explicit pool-only و error،
  registry/cancel، mode/rule generation، loop prevention و private-network policy.
- **آزمون:** تمام سلول‌های جدول حالت؛ pool empty/dead/tag missing؛ direct unaffected؛
  exact/suffix boundary/IP/CIDR/IPv6؛ cancel race؛ تغییر mode هنگام streaming/upload؛
  idle pooled HTTP و hijacked CONNECT و reuse پس از mode/rule change با counter؛
  proxy-to-self و دسترسی به مقصد مدیریت بدون ایجاد loop یا تغییر state؛
  status و redirect بدون تغییر، no request replay، HEAD/POST/body، CONNECT byte forwarding،
  هیچ MITM/تغییر DNS/UDP bypass. first release UDP command را صریح رد کند.

## T4 — pool، واردکردن و رتبه‌بندی

- **نتیجه:** A06/A07، با حفظ fidelity و secrets.
- **وابستگی:** T2/T3 برای dispatch/probe معتبر.
- **دامنهٔ پیشنهادی:** `internal/pool/`, `internal/imports/`, `internal/probes/` و
  persistence؛ importer v2rayN اختیاری و فقط‌خواندنی.
- **کار:** manual/file/URL/v2rayN import؛ node-level validation/quarantine، منبع و
  dedupe canonical، snapshot/export امن، refresh اتمیک با حفظ آخرین دادهٔ سالم.
- **کار:** health startup/cached last-good، rotating samples، cooldown، score/hysteresis،
  انتخاب دستی و refresh/rerank صریح؛ انتخاب خروجی جدید موتور را عمومی restart نکند.
- **آزمون:** duplicate در چند منبع؛ partial invalid، HTML/error/empty source، oversized،
  redirect/auth-secret handling، XHTTP extra/transport params، invalid verification؛
  fixture مخرب با root routing/dns/env/api/inbounds، script، مسیر فایل محلی و outbound
  dependency ناشناس؛ assert reject/quarantine و عدم اجرای کد/خواندن فایل/تغییر state
  خارج از import. XHTTP extra فقط با fields مجاز و به‌عنوان داده حفظ شود؛ موفقیت
  config builder به‌تنهایی شاهد sanitization یا sandbox نیست؛
  stale health، all cooling/dead، local outage vs exit failure، latency noisy، failed
  promotion و rollback بدون direct fallback؛ worker limits و قطع امن probe.
- **محدوده:** remote public pool زنده با درخواست کاربر برای check محدود مجاز است؛
  automated suite باید با fixtures/peerهای محلی کار کند.

## T5 — پیشنهادها و مدیریت کاربر

- **نتیجه:** A05/A09/A10، هیچ تغییر مسیر بدون تأیید.
- **وابستگی:** T3/T4 و قرارداد management/retention در T2.
- **دامنهٔ پیشنهادی:** `internal/diagnostics/`, `internal/control/`, CLI و terminal menu.
- **کار:** eligible failure → safe probe → evidence/proposal؛ accept/reject/recheck،
  پیشنهاد unroute با تأیید؛ command دستی fetch/diagnose با محدودهٔ روشن و no MITM.
- **کار:** rules/pool/status/list/doctor، mode/global direct، import/refresh/rerank،
  env/exec، config و خروجی machine-readable؛ منوی lightweight معادل فرمان‌ها.
- **آزمون:** disable cancels queue؛ failure هیچ rule نمی‌سازد؛ تأیید فقط مقصد انتخابی؛
  no cookie/auth/body/query در probe/evidence؛ IP probe claim محدود؛ TLS failure/EOF/
  cancellation origin errors درست؛ all-proxy هیچ direct comparison پنهانی ندارد؛
  unauthorized management و terminal escape در نام node مهار شوند.

## T6 — نصب، سرویس و اتصال برنامه‌ها

- **نتیجه:** A01/A09/A11/A13 روی ماتریس منتخب Linux.
- **وابستگی:** T2–T5.
- **دامنهٔ پیشنهادی:** `cmd/routelatch/`, `internal/platform/`, `deploy/`, packaging و README.
- **کار:** foreground و systemd user unit؛ wrapper مستقل، health identity و env cleanup؛
  desktop proxy opt-in، حفظ مقدار قبلی، ownership-aware restore و offline recovery.
- **کار:** self-contained binary/package؛ version/upgrade policy pinned، private state
  directories، log rotation/retention، config migration و backup قابل بازگشت.
- **آزمون:** ماشین/محیط پاک بدون skill/websearch/runtime دستی؛ login lifecycle و restart؛
  foreign listener، config corrupted، permissions ضعیف، graceful shutdown؛ inactive
  launcher هیچ silent direct injection نکند؛ set/restore و user-edited settings؛
  محیط فرزند launcher با HTTP_PROXY/HTTPS_PROXY/ALL_PROXY/NO_PROXY ارث‌رسیده و
  قواعد متعارض آزموده شود؛ برنامه‌های مستقلاً اجراشده خارج از تضمین cleanup ما هستند؛
  update خراب rollback داده را حفظ کند. نصب/حذف هیچ سرویس/فایل نامرتبط را عوض نکند.
- **اجازه:** نصب/تغییر proxy دسکتاپ/فعال‌کردن سرویس روی ماشین کاربر فقط با درخواست
  صریح؛ تست restore ترجیحاً در محیط مجاز ایزوله، نه تنظیم واقعی فعلی.

## T7 — پذیرش یکپارچه و آمادگی انتشار

- **نتیجه:** A01–A14، با گزارش واقعیِ passed/failed/not run و review مستقل.
- **وابستگی:** T1–T6.
- **کار:** تمام مسیر کاربر از install → import → rule → use → proposal → mode switch
  → pool failure → all-direct → stop/recovery را اجرا کن؛ تستِ فقط mock کافی نیست.
- **کار:** leak checks با counter/listenerهای direct و pool کنترل‌شده، سقف منابع،
  slow clients، streaming، crash/cancel و secret scan؛ معیار عددی از T2 نه ادعای حدسی.
- **کار:** مجوز D20 در R3 بسته شد؛ در T7 فقط artifact نهایی (notices، source offer و
  integrity) و نام/registry/trademark دوباره بررسی شود؛ public repository/publish/push
  نیاز به مجوز جدا دارد.
- **دروازه:** reviewer مستقل باید کل coherent change را با guidance مناسب بررسی کند؛
  یافته‌های مادی رفع و effects بررسی شوند؛ verdict و محدودیت اجرای پلتفرم‌ها گزارش شود.

## Milestoneهای بعدی — خارج از v1 (تصمیم کاربر ۲۰۲۶-۱۱-۲۰)

این چهار مورد **کار باز v1 نیستند** و به تصمیم/مجوز تازه و یک milestone جدا نیاز دارند
(D24؛ «آزمون‌نشده» به‌معنای «در صف v1» نیست):

1. **Windows:** build واقعی، named-pipe/ACL مدیریت، SCM و proxy settings/restore؛
   chmod لینوکس کافی نیست. scope دقیق با کاربر بسته شود.
2. **UDP و TUN:** semantics جدید، DNS capture/split/FakeDNS در صورت نیاز، IPv6 و
   loop/permissions، crash recovery، per-app coverage و mode-switch session control.
   وجود TUN در Xray اثبات تکمیل این مرحله نیست. سیاست فعلی no-UDP/QUIC است
   (`requirements.md:98`، `architecture.md:60`).
3. **Android:** `VpnService`/foreground/per-app/protect/socket، VPN coexistence limits،
   یک Go runtime در artifact، policy مشترک و GUI بومی یا چندسکویی پس از تصمیم.
4. **GUI:** مصرف management contract مشترک؛ انتخاب framework و سرمایه‌گذاری TUI
   خارج از نسخهٔ اول و نیازمند تصمیم جدید است.

## پوشش پذیرش و recovery

| معیار | task و check یکپارچه |
| --- | --- |
| A01/A13 | T6، نصب پاک و lifecycle در T7 |
| A02/A03/A04 | T1/T3، counters عدم bypass و mode races در T7 |
| A05 | T1 پیش‌نیاز feedback؛ T5/T7 پذیرش proposal/approval/disable/no replay |
| A06/A07 | T4، refresh failures و pool churn در T7 |
| A08/A12 | T1/T3، DNS/HTTP/stream/UDP boundaries در T7 |
| A09 | T2–T6، private management، perms، terminal/log/secret checks در T7 |
| A10/A11 | T5/T6، CLI/menu/launcher/desktop recovery در T7 |
| A14 | **مجوز/ممیزی/بسته بسته (R3+R8)** | Apache-2.0، ممیزی ۶۸ ماژول، بستهٔ release با checksum و source offer؛ طبق تصمیم کاربر بررسی رسمی نام لازم نیست، ایمیل committer قابلانتشار است، و push مجاز است — ./release-readiness.md |

دادهٔ dev ساخته‌شده برای بازبینی دستی حذف نشود. import قدیمی read-only؛ upgrade
از state قبلی backup خصوصی و schema version داشته باشد. rollback موتور و state
باید compatible باشد؛ حذف broad یا reset برای تست ممنوع است. credential در commit،
fixture، report یا سرویس بیرونی قرار نگیرد.

<a id="open-work"></a>

## کارهای باز و وضعیت ادامه

این بخش مرجع واحد ادامهٔ کار در جلسه‌های بعد است؛ وضعیت در `AGENTS.md` یا سند
جداگانه تکرار نشود. هنگام شروع/پایان task، وضعیت، شاهد check و قدم بعدی اینجا
به‌روز شوند. درخواست «کارهای باز را شروع کن» فقط taskهای رفع‌مانع‌شده را مجاز می‌کند؛
نصب سرویس، تغییر شبکه/دسکتاپ، خروجی عمومی و انتشار همیشه مجوز جدا می‌خواهند.

### وضعیت فعلی

> **توجه به هشها:** تاریخچه در انتشار بازنویسی شد؛ هشهای کوتاهی که در ردیفهای پایین
> برای کارهای R1–R10 آمدهاند مربوط به قبل از بازنویسیاند و در مخزن عمومی وجود
> ندارند. مرجع معتبر، HEAD انتشارشده است.
  **بازخوانی ۲۰۲۶-۱۱-۲۱ (ارکستریتور، پایان چرخهٔ مستندات):** `git status` پاک و HEAD `15d2acb`. این چرخه reuse طولانی‌مدت ترنسپورت (ب/۱)، hysteria2 obfs salamander/gecko و هر دو حالت منفی (ب/۲)، اثبات sidecar از بستهٔ منتشرشدهٔ v0.1.2 (ب/۴)، corrupt-state زیر unit موقت `systemd --user` (ب/۵)، رفع `missing_obfs_password` در import، بهداشت مستندات/checker و اجرای payload RPM روی amd64 (ردیف ۲۰۲۶-۱۱-۲۱) را بست. snapshot تازه روی همین HEAD: `gofmt -l` خالی، `go vet ./...` و `go build ./...` exit 0، `go test -count=1 ./...` سبز (۱۷/۱۷ پکیج، acceptance ۱۱۵٫۸s) و `go test -count=1 -v ./internal/acceptance/` → **۲۲۲ PASS، ۱۳ NOTE، ۰ FAIL** (۱۲۱٫۹s). عدد ۲۲۲ ردیف قدیمی مربوط به HEAD `db6a50f` است؛ این snapshot مستقل روی `15d2acb` است.
  **بازخوانی ۲۰۲۶-۱۱-۲۰ (ارکستریتور):** `git status` پاک، HEAD `75a7183` (یک commit صرفاً سندپسند بالای `db6a50f`) و باینری نصبشده `/usr/bin/routelatch` دقیقاً v0.1.2 / commit `88d04b1` (همان اصلاح re-apply دسکتاپ) است؛ sidecar `/usr/bin/sing-box` 1.14.4 (با `sing-box version`). در همین بازخوانی `systemctl --user is-enabled/is-active routelatch.service` هر دو `enabled`/`active` و `ExecMainStatus=0`/`NRestarts=0` گزارش کردند؛ `status` سالم (targeted/policy 117، pool ۱۷۶ رکورد / ۱۵۷ usable / ۱ healthy). مسیر `~/.local/bin` در یادداشت قدیمیتر مربوط به نصب پیشین است؛ مرجع فعلی `/usr/bin` است.
  (snapshot تاریخی ۲۰۲۶-۱۱-۱۹: HEAD پیش از R12 فاز ۲ `6a7580f` و commitهای فاز ۲ `3839d6a` + اصلاح `505a650`اند. بررسیها روی معادل قبل از بازنویسی `3425c3f` اجرا و روی `3cb7bd7` پس از بازنویسی تأیید شد —
هنگام شروع، `git log` را ببین)
   **بازخوانی سرد ۲۰۲۶-۱۱-۱۸ (ارکستریتور):** `git status` پاک و HEAD واقعی `b01661d` بود — دو commit صرفاً سندپسند (`4106da5` بستن ادعای کهنهٔ انتشار، `b01661d` ثبت R11) بالای `efc73c3` که رکورد به آن اشاره میکرد. تنها HEAD بهروز شد؛ هیچ کد/سرویس/تنظیم میزبان تغییر نکرد. commit بازخوانی: `a18dd97`.)

- **رد gecko بدون رمز obfs در import (۲۰۲۶-۱۱-۲۱، ارکستریتور — رفع یافتهٔ ب/۲):** لینک `hysteria2://…?obfs=gecko` بدون `obfs-password` (یا با مقدار خالی) حالا در import با status `invalid` و دلیل پایدار **`missing_obfs_password`** رد می‌شود؛ پیش از این `unknown` وارد می‌شد و sing-box v1.14.4 بعداً کانفیگ تولیدشده را با «missing obfs password» رد می‌کرد، خلاف invariant «اعتبارسنجی پیش از ساخت engine». تغییر در `internal/imports/record.go` (دروازهٔ `validateNode`) و ثابت/متن دلیل در `internal/imports/sanitize.go` است؛ `obfs` همچنان اختیاری است، salamander بدون رمز رفتار پیشین (`invalid_obfs`) را نگه می‌دارد و نوع ناشناخته هم `invalid_obfs` می‌ماند. همین دروازه برای outbound JSON هیستریا۲ هم کار می‌کند. قرارداد عمومی در `docs/contracts.md` §۵.۴ ثبت شد. **شواهد:** تست `TestHysteria2ObfsPasswordRequired` پیش از تغییر روی gecko بدون رمز `status=unknown` داد و پس از تغییر pass (شامل `hy2://`، alias `obfsPassword` و رمز خالی)؛ `gofmt -l` روی چهار فایل Go تغییریافته خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./internal/imports/...` سبز؛ `go test -count=1 ./...` سبز (۱۷/۱۷ پکیج، acceptance **۱۱۶٫۹۹۷s**، گیت‌ها خاموش). **بازبینی مستقل:** `Ready with nits` — یک نادرستی سند در توضیح مکانیسم دادهٔ قدیمی و دو nit (کامنت فیلد قرارداد، پوشش alias شامل حالت مثبت `obfsPassword`) رفع شدند؛ دور تأیید: **Ready** (تنها nit باقی‌مانده، مثبت‌کردن تست alias بود که همان‌جا اعمال شد). **باقی (که بعداً رفع شد — «تصمیم‌های باز» مورد ۱، ۲۰۲۶-۱۱-۲۱):** در آن زمان رکورد gecko بدون رمزی که پیش از این تغییر ذخیره شده بود در import جدید ساخته نمی‌شد؛ اگر در state قدیمی می‌ماند، دو validator آن دورهٔ engine/singbox (که رمز را فقط برای salamander اجباری می‌کردند) آن را نمی‌گرفتند و هنگام Sync کانفیگ sidecar، sing-box آن را رد می‌کرد و همهٔ exitهای hysteria2 با `sidecar_unavailable` کنار گذاشته می‌شدند — fail-closed و بدون direct، ولی بدون جداسازی per-record. رفع بعدی مشتق‌سازی `unsupported`/`missing_obfs_password`، کنارگذاری per-record و validator جدید gecko را اضافه کرد، بدون migration یا بازنویسی state. commit محلی: `fix(imports): reject gecko obfs without obfs-password`.

- **اجرای payload باینری RPM روی amd64 (۲۰۲۶-۱۱-۲۱، worker؛ ثبت توسط ارکستریتور):** یک RPM واقعی `x86_64` از HEAD `42b9e4e` در container یک‌بارمصرف `ubuntu:26.04` ساخته شد: داخل آن Go 1.27.4، `GOPROXY=off`، `scripts/build.sh` و `scripts/package.sh` با `ARCHES=amd64` (sidecar از `dist/singbox`، `REQUIRE_SINGBOX=1`) اجرا شد و سپس `rpmbuild` 6.0.1 اسپک تولیدشده را ساخت. خروجی `routelatch-0.1.2.8.g42b9e4e-1.x86_64.rpm` با Version `0.1.2.8.g42b9e4e`، Release `1`، Arch `x86_64`، اندازهٔ ۱۱۷٬۱۷۳٬۶۷۳ بایت و sha256 `e83afdf1…4c46da`؛ ۱۴ فایل شامل `/usr/bin/routelatch`، `/usr/bin/sing-box`، `LICENSE`/`NOTICE` در `/usr/share/licenses/routelatch/`، اسناد در `/usr/share/doc/routelatch/` و unit در `/usr/lib/systemd/user/routelatch.service` با `ExecStart=/usr/bin/routelatch run`. `rpm -qp --scripts` خالی است: بدون scriptlet، بدون فعال‌سازی سرویس و بدون هیچ ورودی `/etc/systemd`. **اجرای payload:** `bin/routelatch version` → `RouteLatch v0.1.2-8-g42b9e4e`، commit `42b9e4e`، exit 0؛ `--json version`، `--help` و `doctor` هر سه exit 0 (هشدارهای doctor فقط «سرویس در حال اجرا نیست» و «pool خالی»)؛ `bin/sing-box version` → 1.14.4 / `c005f353`، exit 0. `rpm -i --nodeps` exit 0؛ `/usr/bin/routelatch version` پس از نصب همان خروجی بود؛ `rpm -V` فقط وابستگی عمداً ردشدهٔ `ca-certificates` را گزارش کرد. **حدود:** systemd واقعی/شروع سرویس در container اجرا نشد، بسته امضا نشده است، و `brp-strip` به دلیل نبود `strip`/`objdump` رد شد (باینری از قبل `-s -w` است). **پاک‌سازی:** container و دایرکتوری‌های موقت حذف شدند، چیزی روی host نصب نشد و در آن واحد هیچ نوشتنی در repo انجام نشد. **وضعیت:** ادعای «اجرای payload روی amd64» بسته شد؛ arm64/سایر معماری‌ها و نصب واقعی + سرویس تحت RPM همچنان آزمون‌نشده‌اند.

- **رفع مشکل `desktop proxy set` بعد از ویرایش دستی (۲۰۲۶-۱۱-۲۰، ارکستریتور):** کاربر گزارش کرد که پس از apply، تغییر دستی تنظیمات پروکسی دسکتاپ باعث میشود `set` مجدداً با پیام «اول restore کن» رد شود. تصمیم صریح کاربر: `set` دوباره اعمال کند و فقط کلیدهای ویرایششدهٔ کاربر را بهعنوان نقطهٔ restore جدید ثبت کند و بقیهٔ کلیدها Previous اصلیشان را حفظ کنند. پیادهسازی در `internal/platform/desktop.go`: مسیر `Previous.Applied` بهجای رد کردن، مقادیر زنده را میخواند و در صورت تغییر، marker تازه (با `Previous` = مقدار دستی کاربر تنها برای کلیدهای ویرایششده، و Previous اصلی برای بقیه) را قبل از نوشتن ذخیره و سپس مقادیر هدف را مینویسد؛ حالت بدون تغییر no-op میماند و تغییر محیط دسکتاپ یا listenerها همچنان مستلزم restore دستی است. حلقهٔ نوشتن/rollback برای یکسانشدن apply تازه و re-apply به helper استخراج شد. تست ردکنندهٔ قدیمی با چهار تست re-apply جایگزین شد (ثبت نقطهٔ restore، rollback تا مقدار کاربر، رد مقدار غیرقابل restore، لغو هنگام شکست ذخیرهٔ marker). اسناد `docs/contracts.md`، `docs/requirements.md` و README بهروز شدند. **شواهد:** `gofmt` پاک؛ `go vet ./...`، `go build ./...` و `go test -count=1 ./...` سبز (۱۷/۱۷ پکیج، acceptance ۱۱۷s). **اجرا نشد:** جریان زندهٔ گیتشدهٔ دسکتاپ (`ROUTELATCH_DESKTOP_TEST=1`)، چون تغییر تنظیمات دسکتاپ میزبان مجوز جدا میخواهد؛ روی نصب فعلی چیزی اعمال نشد. **بازبینی مستقل:** دور اول `Not ready` با نقص Blocking (جایگزینی `Previous` تمام کلیدها با مقدار فعلی، باعث میشد `restore` کلیدهای دستنخورده را به وضعیت RouteLatch برگرداند) و ۴ nit؛ نقص مادی و nitها در همان مرحله رفع و تست رگرسیون `TestKDERestoreAfterReapplyReturnsEveryKey` اضافه شد. شواهد مجدد: تمام پنج تست جدید روی کد قبلی (`HEAD:desktop.go`) fail و روی کد فعلی pass؛ `go vet`/`go build`/`go test -count=1 ./...` سبز (۱۷/۱۷، acceptance ۱۱۷s). دور دوم بازبینی **Ready** بود (تنها nit غیرمسدودکنندهٔ متن کامنت هم رفع شد). **باقی:** هیچکدام — commit در `88d04b1` و همان کد بهعنوان v0.1.2 در `/usr/bin/routelatch` نصب و سرویس روی آن active است (بازخوانی ۲۰۲۶-۱۱-۲۰).

- **ریلیز `v0.1.2` منتشر شد (۲۰۲۶-۱۱-۲۰، با مجوز صریح کاربر):** تگ annotated `v0.1.2` (شیء تگ `3d1c181a`) روی commit اصلاح پروکسی دسکتاپ `88d04b1` ساخته و همراه با commit به هر دو remote (`github`، `origin`/forgejo) push شد. اجرای سبز CI `44736109624` روی `88d04b1` (هر شش job شامل `rpm-packages`) دروازهٔ انتشار بود. بستهها در `DIST` ایزوله با `VERSION=v0.1.2`، `COMMIT=88d04b1` و `SOURCE_DATE_EPOCH=1700000000` ساخته شدند؛ `check-artifact.sh` و `check-package.sh` pass، دو اجرای packaging همان `SHA256SUMS` بایتیکسان دادند و `sha256sum -c` سبز بود. چهار RPM باینری `routelatch-0.1.2-1.*` با ابزار استخراجشدهٔ rpm 6.0.1 (نصب نشده) ساخته و از نظر متادیتا، فهرست فایلها و نبود scriptlet بازرسی شدند. نخستین تلاش آپلود با `urllib` پیش از ذخیرهٔ هیچ دارایی timeout خورد؛ ریلیز نگه داشته شد و ۱۷ دارایی با `curl` جریانپذیر آپلود شدند. بررسی نهایی API عمومی name/size/digest دقیقاً برابر فایلهای محلی، بدون دارایی اضافی، و تگ annotated روی `88d04b1` را تأیید کرد. جزئیات و checksumها در `docs/release-readiness.md` است.

- **بررسی بعد از ریستارت (۲۰۲۶-۱۱-۲۰، ارکستریتور — خواندنی فقط؛ بدون تغییر کد/سرویس/تنظیمات/داده):** به درخواست کاربر، سیستم ریستارت و سپس یکبار دستی سرویس restart شد. `git status` پاک و HEAD `5e5aa81` است. ژورنال user manager نشان میدهد یونیت `routelatch.service` بعد از بوت ۱۲:۴۳:۱۳ در **۱۲:۴۳:۲۷**، یعنی پیش از restart دستی ۱۲:۵۲:۲۰، **خودکار** شروع شد (PID ۲۰۱۴، `mode=targeted policy=117`، `sidecar.init`). وضعیت فعلی: `enabled`/`active`، `ExecMainStatus=0`، `NRestarts=0`، باینری `~/.local/bin/routelatch` و sidecar `~/.local/bin/sing-box`. `status`: targeted/policy 117، pool ۱۷۶ رکورد / ۱۵۷ usable / ۱ healthy؛ `doctor`: همهٔ controlها ok و تنها هشدار ۱۹ رکورد quarantine (مستند و NEVER dialed)؛ `desktop proxy status`: `kde supported:true applied:true` و previous recorded؛ دو لیسنر دقیقاً loopback و socket management `0600`. ژورنال بعد از بوت فقط هشدار deprecation مربوط به Xray دارد و خطای startup یا failure تازهای وجود ندارد. بدینترتیب قسمت **reboot/re-login** از R1/R7/A13 اثبات شد. **باقی (در آن تاریخ):** corrupt-state تحت systemd (فقط offline)، نشست GNOME و smoke ترافیک واقعی post-reboot (شواهد R7 معتبرند). هیچ سرویس/تنظیم/دادهای تغییر نکرد.

- **بررسی R11 (۲۰۲۶-۱۱-۱۸، ارکستریتور — تحقیق خواندنی فقط؛ بدون مجوز و بدون تغییر کد):** دو عامل مستقل وارد تقسیمشدند، یکی مسیر import/اعتبارسنجی/D-item و دیگری موتور/رانتایم. شواهد سورس: `hysteria2://` در `imports/link.go:41-46` به `unsupported`/`protocol_unsupported` و تنظیمات sing-box در `v2rayn.go:29-34,80` به `invalid` میافتد؛ نقاط تصمیم پروتکل پراکندهاند (`contract/pool.go:16-33`، `imports/{link,v2rayn,record}.go`، `engine/config.go:176-232,343-409`). Xray pinشده (`v26.11.9`، `go.mod:8`) تنها `proxy/hysteria` (هیستریا **v1**) دارد و آن outbound در `main/distro/all/all.go` ثبت نشده و outboundای به نام `hysteria2` در tree نیست → «گسترش موتور pinشده» بهتنهایی R11 را برآورده نمیکند. پشتیبانی واقعی یا رانتایم دوم (sing-box) یا مهاجرت تکموتوره میخواهد؛ جداسازی process-global دو core آزموننشده است (`spike/xray/FINDINGS.md:225`) و اسکیمای sing-box در `docs/research.md` شاهدی ندارد. **پایان تحقیق است، نه شروع R11:** تصمیم معماری + مجوز صریح کاربر اخذ نشده و D-itemها (no MITM، preservation hostname، fail-closed، quarantine) تغییرناپذیراند. آزمون/harness آینده: `internal/acceptance/a06_imports_test.go` (مالک import) و الگوی peer محلی `fixtures_test.go`/`peers_test.go`/`exit_test.go:185`.
   **تصمیم + مجوز کاربر (همان جلسه):** معماری «رانتایم دوم (sing-box) کنار Xray» پذیرفته شد — Xray فعلی برای مسیرهای اثباتشده میماند و sing-box فقط exitهای hysteria2/sing-box را هدایت میکند. D-itemها تغییرناپذیرند. **دروازهٔ قبل از کد:** مجوز sing-box (GPL-3.0?) و اسکیمای معتبر hysteria2 outbound باید از منبع رسمی تأیید شود؛ چون D20 با Apache-2.0 بسته شده، تعارض مجوز احتمالی پیش از هر تغییر کد با کاربر باز میشود.
   **نتیجهٔ دروازه (۲۰۲۶-۱۱-۱۸، دو عامل پژوهشی، منابع رسمی pinشده):** sing-box `v1.14.4` (tag ۲۰۲۶-۱۰-۲۴، commit `c005f353`) تحت **GPL-3.0-or-later** + بند ممنوعیت نامگذاری مشتقات است (`LICENSE` سورس رسمی؛ GitHub `spdx_id=NOASSERTION`) و `SagerNet/sing` هم GPL-3.0 است. در مقابل هستهٔ hysteria2 `github.com/apernet/hysteria/core/v2` در tag `core/v2.14.0` **MIT** است. طبق GPL-3.0 §5(c)/§0، جاسازی sing-box در باینری RouteLatch کل باینری را تحت GPL میبرد و با D20/Apache-2.0 و ممیزی «صفر GPL» تعارض دارد؛ ارسال sing-box بهعنوان **executable جدیگانه** (aggregate، ارتباط arm's-length از طریق socket) این تعارض را حل میکند ولی source offer + notices + رعایت بند نامگذاری و تغییر بستهٔ release را اضافه میکند. اسکیمای معتبر: outbound `hysteria2` در `option/hysteria2.go` فیلدهای `server`/`server_port`/`password`/`tls`(اجباری)/`up_mbps`/`down_mbps`/`obfs` و **بدون `transport`** دارد؛ **هیچ RPC اضافه/حذف outbound در لحظه وجود ندارد** (`daemon/started_service.proto`) → رانتایم دوم با regenerate+reload تنظیم میشود؛ handoff محلی از طریق inbound `mixed`/`socks` و حفظ hostname با حذف قواعد `resolve`؛ URI `hysteria2://`/`hy2` با پورت پیشفرض ۴۴۳ و پارامترهای `obfs`/`obfs-password`/`sni`/`insecure`/`pinSHA256`/`ech` و پهنای باند باید توسط RouteLatch تنظیم شود (از URI خوانده نمیشود). **کد هنوز ننوشته است.** **انتخاب نهایی ارکستریتور (با تفویض صریح کاربر «بهترین حالت رو انتخاب کن»، ۲۰۲۶-۱۱-۱۸):** مسیر ۱ — sing-box `v1.14.4` بهعنوان **executable جدیگانهٔ GPL-3.0** (process جدیگانه، ارتباط arm's-length از طریق inbound محلی) ship میشود؛ باینری RouteLatch Apache-2.0، LICENSE/THIRD-PARTY-NOTICES و ممیزی ۶۸ ماژول تغییر نمیکنند. تعهدات اضافی: notices/source offer GPL-3.0 + بند نامگذاری + بستهٔ release حاوی باینری دوم. مسیر ۳ (جاسازی + تغییر مجوز کل باینری) رد شد چون مخزن عمومی قبلاً تحت Apache-2.0 منتشر شده و بازپسگیری آن مجوز معتبر نیست. D-itemها دستنخوردهاند.)

- **R11 انجام شد (۲۰۲۶-۱۱-۱۹):** import `hysteria2://`/`hy2://` و outbound JSON sing-box، فیلدهای typed قرارداد، رانتایم `internal/singbox` با handoff محلی SOCKS5، و اتصال به engine/control — در commit **`84efe5e`**. انطباق GPL جدیگانه و بستهبندی در **`654cbb6`** و هارنس/اثبات ترافیک در **`8d4118b`**. شواهد: `go build ./...` و `go vet ./...` سبز؛ `go test -count=1 ./...` **۱۷/۱۷ پکیج سبز**؛ `go test -race -count=1 ./internal/engine/...` سبز؛ سلول گیتشدهٔ `RL_ACCEPT_HYSTERIA2_TEST=1 go test -count=1 -v -run TestR11Hysteria2RealTraffic ./internal/acceptance/` **pass (~۲۶s)** با باینری رسمی pinشده (archive sha256 `699a1b51…c24968` همخوان با `scripts/package.sh`)، handshake TLS واقعی loopback، echo بایتیکسان با **دقیقاً ۱ تماس مقصد**، exit مرده/نبودن sidecar → 502 با ۰ تماس و بدون direct، مقصد تأییدنشده → direct، quarantine `insecure_tls_removed`، و canary D21 غایب. بازبینی مستقل: کد `approve-with-nits` (نقص مادی نشت process sidecar + ۴ nit رفع و توسط همان بازبین تأیید)، packaging `approve-with-nits` (۴ nit رفع و تأیید)، acceptance `approve-with-nits` (بدون تغییر لازم). **باقی/آزموننشده:** فقط یک مسیر server/cert loopback؛ QUIC roaming و session طولانی آزمون‌نشده؛ (به‌روزرسانی ۲۰۲۶-۱۱-۲۱: sidecar بستهٔ منتشرشدهٔ v0.1.2/amd64 آزموده و بایت‌به‌بایت با آرشیو pin‌شده یکسان یافت شد — بخش ب/۴)؛ وضعیت CONNECT هنگام exit مرده timing-دپندنت است (۰ تماس در همهٔ اجراها ثابت، immutable payload مبنای pass). باینری آزمون در `/tmp/opencode` است؛ در خود R11 چیزی نصب/تغییر نشد و اعمال در نصب فعلی یک مرحلهٔ جداگانهٔ مجاز است.)

- **R12 فاز ۱ انجام شد (۲۰۲۶-۱۱-۱۹؛ `1743044` + اصلاح `d206130`):** طبق مجوز و تصمیم کاربر، `.deb` اولویت این فاز بود؛ `scripts/build.sh` حالا چهار معماری `amd64`/`arm64`/`riscv64`/`ppc64le` را cross-build می‌کند و `scripts/package.sh` کنار tarballها `.deb` بدون امضا با `SHA256SUMS` می‌سازد (بدون GPG/مخزن apt). کنترل: `Package: routelatch`، نسخهٔ نرمال‌شدهٔ Debian، `Architecture`، `Depends: ca-certificates` و description؛ payload ریشهٔ بسته را به `/usr` نگاشت می‌کند و postinst/service enable ندارد. نام Debian بستهٔ پاورپی‌سی `ppc64el` است. `scripts/check-package.sh` مجموعهٔ دقیق `SHA256SUMS`، metadata، control archive بدون maintainer script، مجموعهٔ دقیق payload، mode/owner و اعتبار نام معماری Debian را می‌سنجد و CI job جدید چهار cross-build + package را اجرا/اعتبارسنجی می‌کند. **شواهد:** چهار tarball و چهار `.deb` ساخته شد، `check-artifact.sh` برای amd64 و `check-package.sh` برای هر چهار معماری pass، دو بار packaging همان `SHA256SUMS` را داد، تست‌های منفی (حذف خط checksum، postinst تزریقی، فایل اضافه، mode غلط) همه fail شدند، و `go build`/`go vet`/`go test` سبز (۱۷/۱۷). بازبینی مستقل یک نقص Blocking (نام معماری Debian برای پاورپی‌سی) و یک شکاف verification یافت؛ هر دو در همین فاز اصلاح شدند. `arm64` همچنان runtime-untested و `riscv64`/`ppc64le` فقط cross-build/runtime-untestedاند؛ sidecar فقط amd64/arm64. **فاز ۲ (در آن زمان بعدی؛ سپس انجامشده — ردیف R12):** RPM؛ `exe`/Windows تا implementation-ready شدن milestone ویندوز وارد نمی‌شود. تگ/ریلیز `v0.1.0` دست‌نخورده است. **توجه:** قانون نسخهٔ Debian در این رکورد مربوط به فاز ۱ است و در فاز ۲ با نرمالسازی واحد جایگزین شد.
- **R12 فاز ۲ انجام شد (۲۰۲۶-۱۱-۱۹؛ `3839d6a` + اصلاح `505a650`):** طبق مجوز کاربر، RPM با سیاست یکسان `.deb` (بدون امضا، تنها `SHA256SUMS`) اضافه شد. `scripts/package.sh` حالا `rpm_arch_for()` (`amd64`→`x86_64`، `arm64`→`aarch64`، `riscv64`/`ppc64le` بدون تغییر)، نرمالسازی واحد نسخه (`v0.1.0-r12.2` → `0.1.0.r12.2`) و چهار اسپک RPM را کنار tarball/debها در `SHA256SUMS` ثبت میکند؛ هر اسپک Source0 را با SHA-256 پین میکند، LICENSE/NOTICE را در `/usr/share/licenses/routelatch/` و بقیه اسناد را در `/usr/share/doc/routelatch/` نصب می کند، unit استاندارد را همانند `.deb` به `/usr/bin/routelatch run` بازنویسی میکند و هیچ scriptlet یا فعالسازی سرویسی ندارد. `scripts/check-package.sh` تمام کنترلهای فاز ۱ را حفظ کرده و مجموعهٔ دقیق ۱۲ عضو `SHA256SUMS`، متادیتا/ساختار اسپک (نسخه، معماری، منبع، `%files`) و هشت تست منفی (نسخه، معماری، `%post`، digest، محل LICENSE، sidecar، حذف اسپک، حذف بازنویسی unit) را میسنجد. **شواهد:** چهار باینری، چهار tarball، چهار `.deb` و چهار اسپک ساخته شدند؛ `check-artifact.sh` برای amd64 و `check-package.sh` برای هر چهار معماری (با و بدون sidecar) pass؛ دو اجرای packaging همان `SHA256SUMS` را داد؛ `go build`/`go vet`/`go test -count=1 ./...` سبز (۱۷/۱۷)؛ YAML و shell syntax چک شدند. **بازبینی مستقل:** دور اول `Not ready` با یک نقص Important (`ExecStart` اسپک به مسیر `%h/.local/bin` اشاره میکرد) و یک nit (نرمالسازی ناقص در CI)؛ هر دو در `505a650` رفع و توسط همان بازبین **Ready** (در حدود محدودیتهای فاز) تأیید شد. **باقی/آزموننشده:** rpmbuild/rpm/rpmspec محلی نبودند و نصب نشدند؛ باینری RPM محلی ساخته/بازرسینشده و jobCI `rpm-packages` اجرا نشده؛ `arm64` cross-build/runtime-untested و `riscv64`/`ppc64le` فقط cross-build؛ sidecar فقط amd64/arm64. تگ/ریلیز `v0.1.0` دستنخورده و انتشار مجوز جدا میخواهد. **به‌روزرسانی ۲۰۲۶-۱۱-۲۱:** باینری RPM با rpm 6.0.1 ساخته/بازرسی شد، در `v0.1.1`/`v0.1.2` منتشر شد و payload `x86_64` نسخهٔ `0.1.2.8.g42b9e4e` (HEAD `42b9e4e`، پس از تگ v0.1.2) در container یک‌بارمصرف نصب/اجرا شد (ردیف اجرای payload RPM)؛ jobCI `rpm-packages` در `44682457124` سبز شد.

- **اولین ریلیز `v0.1.0` و شکست CI (۲۰۲۶-۱۱-۱۹):** تگ `v0.1.0` روی `068e9c2` ساخته و به هر دو remote push شد؛ Release عمومی در `https://github.com/m-hosseinpour/routelatch/releases/tag/v0.1.0` با سه دارایی: `routelatch-v0.1.0-linux-amd64.tar.gz` (sha256 `94d25d04…7667cf`)، `routelatch-v0.1.0-linux-arm64.tar.gz` (`7b3dca81…bd96f`) و `SHA256SUMS`. هشarchiveهای sing-box (`699a1b51…`/`436441f7…`) و `sha256sum -c` قبل از آپلود تأیید شدند. **دو اجرای CI روی `068e9c2` شکست خوردند** (`44479272981`، `44479298380`)، ولی نه به علت R11: تست `TestForeignPeerIsRejectedBeforeHandler` flakeای پیشین است. بازبینی/sورس: سرور envelopeٔ `peer_rejected`را مینویسد و بدون خواندن درخواست میبندد؛ write کلاینت با بستن رقابت میکند، `EPIPE` میدهد و `timeoutOr` آنرا به «socket not available» تبدیل میکند (دیال خود سالم بود؛ errno واقعی در CI مشاهده نشد). پیش از رفع ۵/۵۰۰۰ شکست و worktreeهای قبل و بعد از R11 سبز بودند. رفع در **`f3909b5`**: فقط خطای دیال → `ErrNotRunning`؛ کلاینت پاسخ بافرشده را حتی پس از خطای write میخواند تا ردّ peer برگردد. تشخیص socket (`Lstat` + probe خام) و تست رگرسیون اضافه شد؛ assertionها و `handler.calls==0` دستنخوردهاند. بازبینی مستقل `approve-with-nits` (کامنت منسوخ رفع شد). شواهد: `CGO_ENABLED=0 -count=200` در ۲۰ دسته (۴۰۰۰ تکرار)، race ۳۰، Full `go test ./...` ۱۷/۱۷، gofmt/vet سبز. `f3909b5` به هر دو remote push شد؛ اجرای CI جدید `44485453743` روی `f3909b5` **success** تأیید شد (هر دو job: `Build, vet and test` و `Cross-build linux/arm64` سبز). باینری Release مربوط به تگ `068e9c2` است، نه `f3909b5` — پچریلیز جداگانه در صورت درخواست کاربر لازم است.)

- **ریلیز `v0.1.1` منتشر و اصلاح شد (۲۰۲۶-۱۱-۲۰):** تگ annotated `v0.1.1` (شیء تگ `28d1cf89`) روی `2f6381430be9b39aeb736cd77566986a16354fa7` (`2f63814`) ساخته و به هر دو remote push شد. اجرای سبز CI `44682457124` روی `d443bd0` (همهٔ ۶ job شامل `rpm-packages`) مبنای اسپکها بود؛ تنها تفاوت `d443bd0..2f63814` دو خط سند است. بستهها در `DIST` ایزوله با `VERSION=v0.1.1`، `COMMIT=2f63814` و `SOURCE_DATE_EPOCH=1700000000` ساخته شدند؛ `check-artifact.sh` و `check-package.sh` pass و sidecar پینشدهٔ v1.14.4 فقط در amd64/arm64 قرار گرفت. چهار RPM باینری `routelatch-0.1.1-1.*` از اسپکهای v0.1.1 با ابزار استخراجشدهٔ rpm 6.0.1 (نصب نشده) ساخته و بازرسی شدند. **بار اول آپلود ناقص/اشتباه بود:** چهار RPM با نسخهٔ مخصوص CI یعنی `0.0.0.ci-1` و مشاهدهٔ موقت نبودن tarball riscv64 و بستههای `.deb`. پنج دارایی اشتباه (چهار RPM و `SHA256SUMS`) از همین ریلیز حذف و با RPMهای `0.1.1` و `SHA256SUMS` جدید جایگزین شدند؛ بقیهٔ آپلودها به پایان رسیدند. بررسی نهایی API عمومی ۱۷ دارایی را از نظر نام، اندازه و digest دقیقاً برابر با فایلهای محلی تأیید کرد؛ ریلیز عمومی، غیردraft و تگ روی کامیت موردنظر است. تگ/ریلیز با مجوز صریح کاربر انجام شد. جزئیات و checksumها در `release-readiness.md` است.

- **شکست `rpm-packages` در CI و رفع آن (۲۰۲۶-۱۱-۲۰):** push HEAD `bb7050e` اجرای `44680264412` را آغاز کرد؛ ۵/۶ job سبز، ولی `rpm-packages` پس از ساخت موفق `x86_64` در `rpmbuild --target aarch64` با `error: No compatible architectures found for build` شکست و run هیچ artifactای نداشت (`total_count 0`). لاگ job از Actions API خوانده و علت تأیید شد: اسپک تولیدشده دارای `BuildArch: aarch64` است. بازتولید در کانتینر disposable `ubuntu:26.04` با rpm 6.0.1 (همان نسخهٔ runner) نشان داد `BuildArch` بیگانه عامل خطاست؛ همان اسپک بدون `BuildArch` با `--target` برای چهار معماری باینری با ARCHtag درست میسازد. رفع در **`d443bd0`**: حذف `BuildArch` از اسپکها (معماری فقط از `--target`)، الزام نبودن آن در `check-package.sh` بههمراه کامنت هدف، و دو `actions/upload-artifact` (`routelatch-packages`، `routelatch-rpms`) بهعلاوهٔ `SHA256SUMS` برای RPMها. **شواهد محلی:** `sh -n` و parse YAML سبز؛ `package.sh`/`check-package.sh` برای چهار معماری pass و دو اجرا `SHA256SUMS` بایتیکسان؛ ساخت و بازرسی rpm 6.0.1 روی چهار RPM واقعی (اسپک/tarball محلی) pass. **بازبینی مستقل:** **Ready**، بدون نقص مادی؛ یک nit اختیاری (محافظ `BuildArch` در checker به حروف کوچک/بزرگ حساس است، حال آنکه tagهای RPM case-insensitiveاند) و یک مورد خارج از_scope (توضیح `BuildArch` در `packaging/README.md` کهنه است). تگ/ریلیز `v0.1.0` دستنخورده. **اجرای CI جدید `44682457124` روی `d443bd0` با `conclusion=success` تمام شد:** هر شش job (`Build, vet and test`، سه cross-build، `packages` و `rpm-packages`) success؛ دو artifact بارگذاری شد: `routelatch-packages` (tarball/deb/spec/SHA256SUMS) و `routelatch-rpms` (چهار RPM همراه SHA256SUMS؛ دروازهٔ `[ "$count" -eq 4 ]` در job سبز است).

- **رفع ارجاع‌های کهنه و سخت‌ترکردن محافظ `BuildArch` (۲۰۲۶-۱۱-۲۱، ارکستریتور):** `docs/README.md` حالا انتشار `.deb`/اسپک/RPM باینری را در `v0.1.1` و `v0.1.2` (هر کدام ۱۷ دارایی) ثبت می‌کند؛ `packaging/README.md` توضیح `BuildArch` را با کد فعلی هم‌راست کرد (بدون `BuildArch`، معماری فقط از `rpmbuild --target`، و علت خطای «No compatible architectures found for build») و ادعاهای کهنهٔ «CI اجرا نشده/فقط CI» و «منتشر نشده» را اصلاح کرد. در `scripts/check-package.sh` محافظ `BuildArch` حالا case-insensitive و با فاصلهٔ ابتدایی/اطراف و جداکنندهٔ `:` یا `=` تطبیق می‌دهد؛ هیچ کنترل دیگری تغییر نکرد. **شواهد:** `sh -n` سبز؛ `check-package.sh` روی artifactهای واقعی v0.1.2 در `/tmp/opencode/routelatch-v0.1.2-dist` pass؛ چهار اسپک صحیح pass و اسپک‌های ساختگی `BuildArch: x86_64`، `buildarch:x86_64`، `BUILDARCH = x86_64`، `  BuildArch : aarch64`، تب/`=` و `BuildArch` تنها همه با پیام روشن fail شدند؛ checker قبلی حالت‌های lowercase/`=`/indented را pass می‌کرد؛ ردیف‌های وضعیت RPM در همین پلن هم که «باینری: CI فقط» می‌گفتند اصلاح شدند. بازبینی مستقل: **Ready with nits**، بدون نقص مادی در این تغییر؛ nit باقی‌مانده (ادعای «not published» در `README.md` ریشه) پیش‌موجود و خارج از دامنهٔ این بست بود.

- **نسخهٔ لینوکسی پیاده و محلی آزموده است:** `internal/*` و `cmd/routelatch` کامل‌اند؛
  T1 (شواهد هسته)، T2 (قراردادها)، T3/T4/T5 (مسیر، pool، پیشنهادها/مدیریت) و بخش
  کدِ T6 (unit/launcher/desktop adapter/بسته‌بندی) انجام شده‌اند.
- **R5 و R6 انجام شدند:** وضعیت مشتق‌شدهٔ undialable حالا صرف‌نظر از status ذخیره‌شده
  (`unknown`، `healthy`، `cooling` منقضی/زنده، `unsupported`/`engine_record_rejected`)
  در view/status/`doctor` به `unsupported` با دلیل importer تبدیل می‌شود، بدون بازنویسی
  state و با شمارش‌های هم‌خوان؛ ترنسپورت‌های WS، gRPC و XHTTP روی TLS با peer محلی
  واقعاً end-to-end آزموده شدند.
- **R3 انجام شد (`7792cac`):** D20 با **Apache-2.0** بسته شد. `LICENSE` (متن کامل)،
  `NOTICE` و `THIRD-PARTY-NOTICES.md` (۶۸ ماژول و ۶ کامپوننت embedشده) اضافه شدند و
  ممیزی واقعی build در `docs/licensing.md` ثبت است: `go list -deps` برای amd64/arm64 و
  `go version -m` روی باینری هر دو ۶۸ ماژول یکسان دادند، `go mod verify` سبز بود،
  صفر مجوز ناشناخته و هیچ GPL/AGPL به‌عنوان مجوز ماژول. سه ماژول MPL-2.0 به‌صورت
  dependency بدون تغییر (هیچ فایل سورس MPL تغییر نکرده) و `juju/ratelimit` با
  LGPL-3.0، استثنای صریح لینک استاتیک و متن GPL-3.0 در notices. بسته‌بندی حالا
  `LICENSE`/`NOTICE`/notices/`docs/licensing.md`/`go.sum` را در
  `share/doc/routelatch/` می‌گذارد و `internal/licensing` drift بین `go.mod` و
  notices را آفلاین می‌گیرد. checks: `go build ./...`، `go vet ./...` و
  `go test -count=1 ./...` سبز (acceptance: 112.8s); ساخت بسته در یک کپی ایزوله
  بررسی شد و هر سه فایل را حمل می‌کند.
- **R1 انجام شد (`e168512`، `b5b155e`، `5f293e8`، `596529f`) با مجوز صریح کاربر:**
  unit واقعی `systemd --user` نصب و lifecycle آن اجرا شد (enable/start/restart/stop،
  MainPID عوض شد، `ExecMainStatus=0`، `status`/`doctor` روی process تحت manager پاسخ دادند،
  `/proc/PID/exe` باینری نصب‌شده بود). دو listener فقط loopback، socket کنترل `0600` در
  dir `0700`، state/log `0600`. port collision واقعی: `ExecMainStatus=6`، `NRestarts=0`،
  listener بیگانه دست‌نخورده، CLI exit 3، بدون fallback؛ `kill -9` → restart در ~۲s،
  `state.json` بایت‌به‌بایت سالم، مقصد تأییدشده همچنان از exit نشست (بدون direct پنهان)
  و با exit خاموش 502 با ۰ تماس. اجرای دوم exit 4 و loop شروع exit 7 با `StartLimitBurst`.
  **دو نقص واقعی که همین آزمون گرفت و رفع شد:** پیام collision و رد exit 7 توسط سقف
  ۱۲۰ رون display بریده می‌شدند. harness گیت‌شدهٔ `TestA13SystemdUserLifecycle`
  (`ROUTELATCH_SYSTEMD_TEST=1`) همین جریان را روی داده/پورت ایزوله اجرا می‌کند.
  **ماشین بازگردانده شد:** unit متوقف/حذف، پورت‌ها آزاد، هیچ سرویس/تنظیم دیگری تغییر
  نکرد و هیچ‌چیز نصب‌شده نماند. **reboot/re-login (۲۰۲۶-۱۱-۲۰) اثبات شد؛ corrupt-state زیر systemd در ۲۰۲۶-۱۱-۲۱
  با unit موقت انجام شد (ب/۵).** A01 همچنان پذیرفته‌نشده است (نصب روی این ماشین اثبات شد، نه محیط پاک).
- **R2 انجام شد (`72835fc`) روی KDE Plasma واقعی:** آداپتر `kwriteconfig6`/`kioslaverc` استفاده شد و
  سرویس `systemd --user` متغیرهای نشست را درست دید (`XDG_CURRENT_DESKTOP=KDE`)؛ daemon از
  shell لخت `supported:false` و `set` exit 7 می‌دهد (حالا مستند). set واقعی روی OS نوشت
  (http/https و socks به پورت‌های routelatch، `ProxyType=1`) و مقدار قبلی را ثبت کرد؛
  restore با **diff بایت‌به‌بایت** به snapshot برگشت و marker `applied:false` شد؛ ویرایش
  دستی کاربر (`:1099`) حفظ شد و `user_modified:[httpProxy]` گزارش شد؛ سرویس خاموش
  **fail closed** بود (۰ تماس مقصد، بدون direct پنهان)؛ `kill -9` → restart ~۲s با state
  سالم و عبور ۲۰۰. **نقص رفع‌شده:** `status` با نشانگر کهنه بازهم مالکیت را ادعا می‌کرد؛
  `Conflicts`/`conflict`/`notice`، ترتیب restore، گزارش marker خراب و خواندن OS بیرون از
  قفل اضافه شد. `TestA11DesktopProxyLifecycle` (گیت `ROUTELATCH_DESKTOP_TEST=1`) زنده pass شد.
  **اجرا نشد:** نشست واقعی GNOME؛ `ftpProxy`/`Authmode` طبق设计中 مدیریت نمی‌شوند.
- **R4 انجام شد (`766edcd`، `da333fe`) با pool خصوصی کاربر (فقط‌خواندنی، هیچ secret در repo/گزارش):**
  نمونهٔ قطعی ۴ لینک (۲ trojan + ۲ vless، ۴ سرور) از pool با ۱۶۵ خط؛ import → ۳ valid و
  ۱ `unsupported`/`engine_record_rejected` (VLESS عمومی بدون TLS، مستندشده)؛ `refresh --all`
  شکل را حفظ کرد؛ transport ۳/۳ موفق ۲۷۴–۳۹۴ms؛ یک exit با `bytes_down=1` مدرک egress داد و
  دو تای دیگر `destination_unconfirmed` بدون cooling؛ targeted یک درخواست کنترل‌شده →
  CONNECT 200، پاسخ HTTP/2 200 با body بایت‌یکسان، **۱ تماس مقصد**، IP خروجی ≠ IP مستقیم؛
  exit مرده/مسدود → 502 «exit unreachable» با ۰ افزایش شمارنده و بدون fallback؛ all-direct و
  stopped مطابق قرارداد؛ grep اختصاصی D21: صفر hit برای مقصد در state/log/CLI. **نقص رفع‌شده:**
  شواهد downlink ترافیک واقعی به health نمی‌رسید (`pool.RecordUsage` مرده بود و exit سالم
  `unknown` می‌ماند) — فرانت‌اند حالا اولین بایت downlink را به یک نمونهٔ coalesced تبدیل می‌کند
  و probeها wrap نشده‌اند. **باقی:** مقاصد دلخواه VLESS/Trojan، پایداری
  طولانی‌مدت/churn و همهٔ اعضای pool اثبات نشده؛ state/کپی حاوی secret حذف شد.
- **R7 انجام شد — استفادهٔ واقعی (بدون تغییر کد):** unit ماندگار نصب و `enable --now` شد
  (`~/.local/bin/routelatch`، فقط `127.0.0.1:8317/8318`، socket `0600`)؛ pool با ۱۶۵ خط
  → ۱۵۵ پذیرفته (۱۴۹ Trojan + ۵ VLESS) و ۱۰ `engine_record_rejected`؛ **۳۴ دامنه از لیست
  کاربر → ۳۴ rule دقیق**، mode `targeted`؛ پروکسی دسکتاپ KDE روی 8317/8318 تنظیم و
  مقدار قبلی با `desktop proxy restore` قابل بازگشت است. سه بررسی واقعی: مقصد تأییدشده از
  exit (۱ تماس، IP خروجی ≠ مستقیم)، مقصد غیرتأییدشده direct، exit مرده → 502 بدون direct.
  **شروع در login و ریستارت واقعی (۲۰۲۶-۱۱-۲۰) اثبات شد** (رکورد بالا).
- **R8 انجام شد (`9061d92`–`13d0ea0`، merge `e098ceb`):** README عمومی انگلیسی با وضعیت
  واقعی، CI (build/vet/test + cross-build arm64)، `CONTRIBUTING`/`SECURITY`/templateها،
  `.gitignore`، بستهٔ release قابل‌بازتولید با checksum/stamp/source offer و
  `docs/release-readiness.md`. اسکن کل تاریخچه (۴۳ commit): **۰ لینک/UUID/token/pool blob
  یا IP خصوصی**؛ تنها یافته یک مسیر `/home/...` در اویانس `spike/` بود که در tree پاک شد
  ولی در تاریخچه می‌ماند. نام: `routelatch.com` یک SaaS نامرتبط است؛ بقیهٔ registryها آزاد →
  نگه‌داشتن نام با تمایز، و بررسی رسمی کلاس ۹/۴۲ پیش از معرفی.
- **R10 انجام شد (`c1a98f9`, `cbc6a87`, `3425c3f`) با مجوز صریح کاربر:** failover پیش از
  ۲۰۰ در CONNECT — بایتهای کلاینت تا اولین بایت مقصد به exit متعهد نمیشوند؛ مرگ تانل
  پیش از داده روی حداکثر ۳ exit خودکار بافر ۶۴ KiB را امتحان میکند و پاسخ فقط از exit
  موفق میرسد. پین دستی فقط ۱ تلاش دارد و جایگزین نمیشود؛ HTTP ساده و SOCKS5 تغییری
  نکردهاند. ریسک پذیرفتهشدهٔ تکرار بایت در D22 ثبت شد. **شواهد زنده:** ۱۶/۳۰ →
  ۹۲/۱۰۰ و آخرین smoke ۱۹/۲۰؛ `-race` پکیجهای frontend/conns/pool/control سبز و fd
  پس از exhaustion به baseline برگشت. باینری نصب و سرویس روزمره restart شد (دو fetch واقعی ۲۰۰).
- **تنظیم محیط کاربر (خارج از repo):** سرور MCP playwright اکنون مستقیم به RouteLatch
  (`--proxy-server=http://127.0.0.1:8317` + متغیرهای proxy) اشاره میکند و wrapper قدیمی
  ۱۰۸۰ برای آن حذف شد؛ تانل ۱۰۸۰ و سایر سرورها دستنخوردهاند. backup پیکربندی در
  `/tmp/opencode/opencode-config-backup.*` و JSON validate شد.
- **انتشار عمومی (`3cb7bd7`) انجام شد:** تاریخچه با `git-filter-repo` در کپی کاری بازنویسی
  شد تا مسیر ماشین `/home/...` در چهار فایل `spike/` پاک شود؛ ۵۶ commit و درخت HEAD完全
  یکسان ماند، نویسندهها/trailerها دستنخورده و اسکن مجدد ۴۹۰ blob فقط همان مسیر را نشان
  داد. backup کامل در `/tmp/opencode/routelatch-backup-pre-rewrite` نگه داشته شد.
  `origin`=forgejo و `github`=GitHub، هر دو push شدند و heads برابر HEAD محلیاند؛
  GitHub توسط API عمومی (`private:false`) تأیید شد. tag/release ساخته نشد.
- **تنظیمات میزبان خارج از repo:** علاوه بر playwright MCP، drop-in سرویس
  `opencode.service.d/proxy.conf` هم از ۱۰۸۰ به ۸۳۱۷ و `routelatch.service` تغییر کرد؛
  NO_PROXY برای API مدل حفظ شد. اثر env تا restart آن سرویس اعمال نمیشود (برای جلوگیری
  از قطع جلسه فعلی restart نشد).
- **شاهد پذیرش (snapshot تاریخی، HEAD آن زمان `e098ceb`):** `go test -count=1 -v ./internal/acceptance/` →
  **۲۱۵ شاهد `PASS`، ۰ شکست و ۱۳ `NOTE`** (اجراهای گیت‌شده/غیرقابل‌اجرا، نه pass
  جعلی)، ~۱۱۶s؛ ۱۶/۱۶ پکیج `go test -count=1 ./...` سبز. `go build ./...`،
  `go vet ./...` و tree پاک. بستهٔ release روی همین HEAD از یک export پاک ساخته و
  تأیید شد (`routelatch-e098ceb-linux-amd64/arm64`، stamp هم‌خوان BUILD-INFO،
  `sha256sum -c` OK)؛ بسته فقط از tree پاک ساخته شود، چون stamp با tree dirty
  `-dirty` می‌شود. پایه ۲۰۸ بود؛ R10 شش assertion اضافه کرد و عدد ۲۱۵ توسط همان worker تأیید شد.
- **چک سلامت مجدد ۲۰۲۶-۱۱-۲۰ (ارکستریتور، HEAD `db6a50f`):** toolchain Go در محیط نصب نبود؛ نسخهٔ رسمی `go1.27.4` (sha256 `7226736c…b24840` تأییدشده با `go.dev/dl/?mode=json`) بهصورت موقت و بدون نصب سیستمی از آینهٔ `mirrors.aliyun.com` در `/tmp/opencode/gotool` استخراج و با `GOROOT`/`PATH`/`GOTOOLCHAIN=local` اجرا شد. شواهد: `gofmt -l $(git ls-files '*.go')` خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./...` **۱۷/۱۷ پکیج ok** (acceptance ۱۱۹.۸s؛ مجموع ~۲:۰۱)، ۰ FAIL؛ `go test -count=1 -v ./internal/acceptance/` → **۲۲۲ شاهد `PASS`، ۱۳ `NOTE`، ۰ شکست** (۱۱۴.۲s). عدد ۲۱۵ مربوط به HEAD قدیمی `e098ceb` است و شامل اضافات R11/R12/v0.1.2 نمیشود؛ ۲۲۲ هم مخصوص همین HEAD `db6a50f` است و snapshot تازهٔ `15d2acb` جداگانه در «وضعیت فعلی» ثبت شد. هیچ کد/سرویس/تنظیم میزبان تغییر نکرد؛ فقط مستندات رکورد بهروز شدند.
- **بازبینی مستقل:** قراردادها (دو دور)، هر واحد پیاده‌سازی، پذیرش، کل تغییر
  (`1416c32..HEAD`)، D21، تغییر ادغام‌شدهٔ R5+R6، R3 (دو دور: embedded notices و
  attribution)، R1، R2 (approve-with-findings → یافته‌های مادی رفع) و R4 (approve؛ دو
  یافته بسته، دو nit غیرمسدودکننده) verdict مثبت گرفتند.
  یافته‌های مادی (F1–F8، bypass وضعیت `cooling`/`healthy`، بریده‌شدن پیام collision،
  holderهای جاافتاده و کامپوننت‌های embedشده، ادعای مالکیت کهنه در desktop status، و
  نرسیدن شواهد downlink واقعی به health) رفع و بازبینی شدند. هیچ یافتهٔ باز
  مسدودکننده‌ای ثبت نشده است.
- **تصمیم‌های کاربر ثبت‌شده:** دروازهٔ handshake (تأیید واقعی هر جا ممکن + پذیرش صریح
  VLESS/Trojan)، ماتریس amd64/arm64 با GNOME/KDE، D21 (هیچ مقصدی در هیچ لاگ/فایل
  ثبت نشود؛ پیشنهادها فقط در حافظه)، مجوز **Apache-2.0** (D20/R3)، و مجوز نصب سرویس
  برای R1. برای R4 اجازهٔ استفادهٔ خواندنی از pool محلی کاربر (بیرون از repo)
  داده شده است؛ آن مسیر و هیچ لینک/credential آن نباید در repo،
  fixture، لاگ یا گزارش ثبت شود.

### کارها، به ترتیب و با وضعیت مجوز

| # | کار | نتیجهٔ متناظر | مجوز لازم؟ |
| --- | --- | --- | --- |
| R1 | **انجام شد** (`e168512`, `b5b155e`, `5f293e8`, `596529f`): unit واقعی `systemd --user` نصب و lifecycle آن اجرا شد — enable/start/restart/stop، `ExecMainStatus=0`، `/proc/PID/exe` باینری نصب‌شده، دو listener فقط loopback، socket کنترل `0600` در dir `0700`؛ port collision واقعی با `ExecMainStatus=6`/`NRestarts=0`/CLI exit 3 و بدون fallback؛ `kill -9` → restart ~۲s با `state.json` سالم و بدون direct پنهان (exit خاموش → 502 با ۰ تماس)؛ اجرای دوم exit 4 و loop شروع exit 7. دو نقص بریده‌شدن پیام رفع شد و `TestA13SystemdUserLifecycle` (گیت `ROUTELATCH_SYSTEMD_TEST=1`) اضافه شد. **ماشین به وضعیت قبل بازگشت و هیچ چیزی نصب نماند. reboot/re-login (۲۰۲۶-۱۱-۲۰) اثبات شد؛ corrupt-state زیر unit موقت در ۲۰۲۶-۱۱-۲۱ انجام شد (ب/۵).** | A13 (اثبات واقعی) و مسیر نصب A01 | انجام شد با مجوز شما |
| R2 | **انجام شد** (`72835fc`) روی KDE Plasma واقعی: آداپتر `kwriteconfig6`/`kioslaverc`، set واقعی روی OS، ثبت مقدار قبلی، restore با **diff بایت‌به‌بایت** به snapshot، ویرایش دستی کاربر حفظ و `user_modified` گزارش شد، سرویس خاموش **fail closed** (۰ تماس مقصد)، `kill -9` → restart ~۲s با عبور ۲۰۰. نقص ادعای مالکیت کهنه در `status` رفع شد؛ `TestA11DesktopProxyLifecycle` (گیت `ROUTELATCH_DESKTOP_TEST=1`) زنده pass شد. **اجرا نشد:** نشست واقعی GNOME؛ `ftpProxy`/`Authmode` طبق تصمیم مدیریت نمی‌شوند | A11 (KDE) | انجام شد |
| R3 | **انجام شد** (`7792cac`، اصلاحیه‌های notices/attribution `4e0dde6`، `61604c5`): D20 با Apache-2.0 بسته شد؛ `LICENSE`/`NOTICE`/`THIRD-PARTY-NOTICES.md` و ممیزی ۶۸ ماژول و ۶ کامپوننت embedشده (amd64+arm64، `go version -m` هم‌خوان) اضافه شدند. صفر مجوز ناشناخته؛ MPL-2.0 فقط dependency بدون تغییر؛ LGPL-3.0 با استثنای لینک استاتیک و متن GPL-3.0؛ جزئیات و محدودیت‌ها در `docs/licensing.md`. **باقی A14:** source offer برای MPL، بستهٔ سورس/انتشار و بازبینی امنیت/نام — بیرون از این تصمیم | A14 (بخش مجوز/ممیزی) | انجام شد — تصمیم شما ثبت شد |
| R4 | **انجام شد** (`766edcd`, `da333fe`) با pool خصوصی کاربر (فقط‌خواندنی؛ هیچ secret در repo/گزارش): نمونهٔ ۴ لینک از ۱۶۵ خط، import ۳/۳ valid + ۱ `engine_record_rejected` مستند، transport ۳/۳ و health/ranking اندازه‌گیری‌شده، یک درخواست targeted با CONNECT 200 و **۱ تماس مقصد** و IP خروجی ≠ مستقیم، exit مرده → 502 بدون fallback، all-direct/stopped مطابق قرارداد، و grep D21 بدون هیچ hit مقصد. نقص «شواهد downlink واقعی به health نمی‌رسید» رفع شد. **باقی:** مقاصد دلخواه، churn/طولانی‌مدت، و همهٔ اعضای pool | A04/A06 (ردیف عمومی، اثبات محدود) | انجام شد |
| R5 | **انجام شد** (`640f83c`, `5d328a6`): مسیر import هرگز `unknown` نمی‌سازد (http → `unsupported`/`http_exit_not_verified`، scheme ناشناس → `unsupported`/`protocol_unsupported`) و view/status حالا رکورد undialable را **صرف‌نظر از status ذخیره‌شده** جز `invalid` مشتق می‌کند. `pool list`، `status` و `doctor` از یک مالک واحد (`EffectiveStatus`) می‌خوانند، state بازنویسی نمی‌شود و رکورد پیش از هر dial/probe رد می‌شود. **Residual پذیرفته‌شده:** protocol تهی/صفر عمداً دست‌نخورده است؛ semantics پروتکل‌های پشتیبانی‌شده با تنظیمات قابل dial تغییر نکرده (رکورد gecko بدون رمز obfs بعداً زیر همین قاعده مشتق شد — «تصمیم‌های باز»)؛ اولین tick چرخش ۳۰ ثانیه‌ای اکنون در سلول گیت‌شدهٔ long-reuse مشاهده می‌شود ولی چرخهٔ کامل ۱۰ دقیقه‌ای در acceptance درایو نمی‌شود؛ `SyncStatuses` پس از فرمان تغییردهنده ممکن است cooldown منقضی را به `unknown` نرمال کند (bookkeeping قبلی، نه نوشتن مشتق‌سازی) | A06/§5 (کیفیت گزارش) | نه |
| R6 | **انجام شد** (`a69ae1e`): WS/TLS، gRPC/TLS (`h2`) و XHTTP/TLS (`h2`) با exit pin‌شدهٔ محلی، rule، `pool select` و CONNECT به peer loopback واقعاً آزموده شدند (هر سلول دقیقاً ۱ درخواست مقصد و پاسخ بدون تغییر)؛ pin غلط ۰ داده به مقصد می‌فرستد و سرویس بالا می‌ماند. **خارج از v1، نه آزمون‌نشده:** REALITY/ECH، mKCP، HTTPUpgrade و mux/XUDP پشتیبانی نمی‌شوند و h3 به‌دلیل سیاست no-UDP/QUIC کنار گذاشته شده (بخش «تفکیک محدودیت‌ها»). **reuse طولانی‌مدت انجام شد (۲۰۲۶-۱۱-۲۱):** سلول گیت‌شدهٔ `RL_ACCEPT_LONG_REUSE_TEST=1` روی WS/TLS: ۳ درخواست در ۳۲٫۰s با شکاف بی‌کار ۳۰٫۰s، ۱ اتصال مشتری که کل پنجره زنده ماند و هر سه درخواست را حمل کرد، ۱ درخواست مقصد در هر تلاش، بدون تحویل direct (نام fixture روی host resolve نمی‌شود؛ ۱ اتصال مقصد)؛ اولین probe سلامتِ زمان‌بندی‌شده (بررسی tick ۳۰s؛ چرخهٔ بعدی ۱۰ دقیقه) یک اتصال کوتاه‌عمر جدا با ۰ تماس مقصد ساخت | T1/T4 دقت ادعا | نه |
| R7 | **انجام شد — استفادهٔ واقعی و ماندگار:** نصب unit و `enable --now`، pool کاربری import شد، ۳۴ قاعده برای دامنه‌های کاربر، mode `targeted`، پروکسی دسکتاپ KDE روی RouteLatch با مسیر بازگشت. سه بررسی واقعی (approved→exit، unapproved→direct، exit مرده→502) pass. **شروع در login و reboot (۲۰۲۶-۱۱-۲۰) اثبات شد** | A11/A13 در استفادهٔ واقعی + تجربهٔ dogfood | انجام شد با مجوز شما |
| R8 | **انجام شد (`e098ceb` merge):** README/CI/فایل‌های عمومی، بستهٔ release با checksum/stamp/source offer، اسکن تاریخچه (پاک)، بررسی نام، `docs/release-readiness.md`. | آمادگی انتشار | انجام شد؛ push بعداً با مجوز کاربر انجام شد |
| R10 | **انجام شد (`c1a98f9`, `cbc6a87`, `3425c3f`):** failover CONNECT پیش از ۲۰۰ با سقف ۳ exit و بافر ۶۴ KiB، پین دستی تکـتلاش، بدون direct و بدون تغییر HTTP ساده/SOCKS5؛ ۱۶/۳۰ → ۹۲/۱۰۰ و smoke ۱۹/۲۰؛ نصب و restart سرویس روزمره | A02/A03/D22 | انجام شد |
| R11 | **انجام شد (`654cbb6`، `84efe5e`، `8d4118b`):** پشتیبانی واقعی hysteria2/sing-box با رانتایم جدیگانه sing-box `v1.14.4` کنار Xray — import، فیلدهای قرارداد، handoff محلی SOCKS5، انطباق GPL/بستهبندی، و اثبات ترافیک واقعی (۱ تماس مقصد، fail-closed با ۰ تماس، D21). obfs salamander/gecko با ترافیک واقعی اثبات شد (`internal/acceptance/r11_obfs_test.go`). sidecar بستهٔ منتشرشدهٔ v0.1.2 (amd64) آزموده شد (بخش ب/۴). **باقی (کار باز v1):** session طولانی؛ QUIC roaming در repo قابل بازتولید نیست (نیازمند تغییر مسیر/اینترفیس واقعی) | D07/A06/A08 (گسترش دامنه) | انجام شد با تصمیم معماری + مجوز کاربر |
| R12 | **انجام شد (فاز ۱: `1743044` + `d206130`؛ فاز ۲: `3839d6a` + اصلاح `505a650`):** `.deb` بدون امضا و اسپک RPM (`amd64`→`x86_64`، `arm64`→`aarch64`، `riscv64`، `ppc64le`→Debian `ppc64el`) در چهار معماری، هرکدام با سطر `SHA256SUMS`؛ نسخهٔ واحد (`0.1.0.r12.x`) دقیقاً در فیلد Version هر دو قالب. باینری RPM از همین اسپک‌ها با rpm 6.0.1 ساخته/بازرسی و در `v0.1.1`/`v0.1.2` منتشر شد؛ payload `x86_64` نسخهٔ `0.1.2.8.g42b9e4e` (HEAD `42b9e4e`، پس از تگ v0.1.2) در container یک‌بارمصرف نصب و اجرا شد (ردیف اجرای payload RPM)؛ jobCI `rpm-packages` هم همهٔ چهار RPM را می‌سازد/بازرسی می‌کند. متادیتا/payload/اسپک/checksum توسط `check-package.sh` آزموده و تست منفی gate RPM اجرا شد | A01/A14 (توزیع) | فاز ۱ و ۲ با مجوز کاربر؛ Windows `exe` خارج ازScope |
| انتشار | **انجام شد (`3cb7bd7` هنگام push، HEAD پیش از R12 فاز ۱ `a028469`):** بازنویسی تاریخچه در کپی کاری، push به forgejo و GitHub، تأیید عمومی بودن GitHub و برابری remote heads | A14 (انتشار) | مجوز کاربر |
| انتشار `v0.1.1` | **انجام شد (تگ روی `2f63814`):** ۱۷ دارایی شامل چهار tarball، چهار `.deb`، چهار اسپک، چهار RPM با نسخهٔ `0.1.1` و `SHA256SUMS`؛ آپلود اول ناقص بود و با حذف پنج دارایی اشتباه و جایگزینی آنها اصلاح شد؛ تأیید نهایی API عمومی نام/اندازه/digest همگی منطبق | A14 (انتشار) | مجوز صریح کاربر |
| انتشار `v0.1.2` | **انجام شد (تگ annotated روی `88d04b1`):** ۱۷ دارایی شامل چهار tarball، چهار `.deb`، چهار اسپک، چهار RPM باینری `0.1.2` و `SHA256SUMS`؛ CI سبز `44736109624`؛ آپلود نخستین تلاش timeout خورد ولی هیچ دارایی ذخیره نشد و با curl جریانپذیر کامل شد؛ تأیید نهایی API عمومی name/size/digest منطبق | A14 (انتشار) | مجوز صریح کاربر |

**قاعدهٔ انتخاب:** R1–R8، R10، R11، R12 و انتشار بسته شدند؛ R9 (انتخاب کاربر) وجود ندارد.
**R12 کاملاً بسته شد:** `.deb` بدون امضا + checksum و اسپک RPM چهار معماری، با نسخهٔ واحد و CIjob ساخت/بازرسی RPM. **هیچ فاز بازی از R12 نمانده است**؛ Windows `exe` تا implementation-ready شدن milestone ویندوز وارد نمیشود. تگ `v0.1.0` / ریلیز GitHub دستنخورده است.

کارهای باز v1 و محدودیت‌های محیط/مجوز در بخش «تفکیک محدودیت‌ها» فهرست شده‌اند؛
گروه ج و هر اقدام میزبان دیگری مجوز جدا می‌خواهند (corrupt-state آفلاین در ۲۰۲۶-۱۱-۲۱ انجام شد و مجوزش مصرف شد). push، تگ و
ریلیزهای `v0.1.1` و `v0.1.2` با مجوز صریح کاربر انجام شدند.

### آنچه پذیرفته/اثبات نشده است

| معیار | وضعیت | دلیل/حد شاهد |
| --- | --- | --- |
| A02، A03، A05، A06، A08، A12 | اثبات محلی | شمارندهٔ تماس مقصد/خروجی و assertion مستقیم در harness؛ A06 شامل مشتق‌سازی legacy و مسیر import هر دو قاعده |
| WS/gRPC/XHTTP روی TLS | اثبات محلی (R6) | peer loopback pin‌شده، ۱ درخواست مقصد، pin غلط با ۰ داده؛ h3 خارج از v1 است (سیاست no-UDP/QUIC)؛ reuse طولانی‌مدت WS/TLS در سلول گیت‌شدهٔ `RL_ACCEPT_LONG_REUSE_TEST=1` آزموده شد (۳ درخواست/۳۲٫۰s، شکاف ۳۰٫۰s، ۱ اتصال مشتری زنده در کل پنجره) |
| A04، A07، A09، A10 | اثبات محلی + نمونهٔ عمومی محدود (R4) | mode switch زنده+در جریان، rerank/cooldown، مجوز فایل/socket، CLI، launcher env، crash/restart، و ۴ exit عمومی با health/ranking واقعی؛ همهٔ مقاصد/اعضای pool نه |
| A13 | **اثبات واقعی (R1 + بررسی ۲۰۲۶-۱۱-۲۰ + corrupt-state ۲۰۲۶-۱۱-۲۱)** | نصب/فعال‌سازی unit واقعی و lifecycle، راه‌اندازی خودکار بعد از reboot با state/pool/desktop-proxy سالم؛ **corrupt-state (JSON بریده/نسخهٔ جدید/غیرقابل‌خواندن) زیر unit موقت `systemd --user` انجام شد (ب/۵): `ExecMainStatus=10`/`NRestarts=0`، فایل و mode دست‌نخورده، بدون listener و بدون direct، و بازیابی مستند `restore`/`chmod` عملی بود. باقی:** فقط همین host/amd64 و unit موقت، نه نصب واقعی کاربر |
| A01 | **پذیرفتهنشده** | مسیر نصب واقعی و launch آن اثبات شد (R1)؛ metadata/payload `.deb` محلی و اسپک RPM (ایستا) تأیید شدند؛ jobCI ساخت/بازرسی RPM سبز (`44682457124`)؛ باینری RPM `0.1.1` ساخته/بازرسی و payload `0.1.2.8.g42b9e4e`/amd64 (HEAD `42b9e4e`، پس از تگ v0.1.2) در container یک‌بارمصرف نصب و اجرا شد (بدون نصب سیستمی روی host)؛ محیط پاک/ماشین تازه و اجرای arm64/riscv64/ppc64le هنوز نه |
| A11 | **اثبات واقعی روی KDE (R2)** | set/restore با diff OS، مالکیت، ویرایش کاربر، crash recovery؛ **اجرا نشد:** نشست واقعی GNOME |
| A14 | **مجوز/ممیزی/بسته (R3+R8+R12)** | Apache-2.0، ممیزی ۶۸ ماژول (چهار معماری cross-build)، tarball/`.deb`/اسپک RPM با `SHA256SUMS` و نسخهٔ واحد؛ R12 `.deb` و اسپکها را محلی تأیید کرد؛ jobCI ساخت/بازرسی RPM اضافه شد (اجرای سبز `44682457124`)؛ ریلیز `v0.1.1` و سپس `v0.1.2` هر کدام با ۱۷ دارایی شامل RPM باینری منتشر و تأیید شدند |
| `.deb` توزیع (R12 فاز ۱) | **ساخت/اعتبارسنجی محلی** | چهار معماری (`amd64`، `arm64`، `riscv64`، `ppc64el`)؛ `check-package.sh` مجموعهٔ دقیق `SHA256SUMS`، control بدون maintainer script، مجموعهٔ دقیق payload، mode/owner و اعتبار نام معماری Debian را میسنجد؛ تکرار بایتیکسان؛ تستهای منفی حذف خط checksum/postinst/فایل اضافه/mode غلط همه fail شدند؛ `arm64` runtime-untested و `riscv64`/`ppc64le` فقط cross-build |
| اسپک/باینری RPM (R12 فاز ۲) | **اسپک: ساخت/اعتبارسنجی محلی؛ باینری: ساخت/بازرسی و اجرای payload روی amd64** | چهار اسپک (`x86_64`، `aarch64`، `riscv64`، `ppc64le`) با Version برابر `.deb`، منبع pin‌شده با SHA256، `%files` شامل LICENSE/NOTICE در `/usr/share/licenses/routelatch/` و بقیه اسناد در `/usr/share/doc/routelatch/`، بدون scriptlet/فعال‌سازی سرویس؛ تست منفی ۷ حالت fail کرد؛ rpmbuild/rpm/rpmspec به‌صورت سیستمی نصب نشدند؛ rpm 6.0.1 استخراج‌شده باینری‌های `0.1.1` را ساخت/بازرسی کرد؛ payload `x86_64` نسخهٔ `0.1.2.8.g42b9e4e` (HEAD `42b9e4e`، پس از تگ v0.1.2) در container یک‌بارمصرف نصب و اجرا شد؛ jobCI `rpm-packages` در اجرای `44682457124` سبز شد |
| hysteria2/sing-box (R11) | **اثبات محلی با ترافیک واقعی** | هارنس گیت‌شدهٔ `RL_ACCEPT_HYSTERIA2_TEST=1`: handshake TLS واقعی با باینری pinشده، echo بایتیک‌سان، ۱ تماس مقصد، fail-closed ۰ تماس، D21؛ فقط loopback. obfs salamander/gecko با سلول گیت‌شدهٔ `RL_ACCEPT_HYSTERIA2_OBFS_TEST=1` اثبات شد؛ **باز v1:** session طولانی؛ sidecar بستهٔ منتشرشدهٔ v0.1.2/amd64 آزموده شد (بخش ب/۴)؛ QUIC roaming در repo بازتولیدپذیر نیست؛ مقصد/exit عمومی در گروه ج است |
| wire-level replay، UDP واقعی، مقاصد دلخواه، churn و خروجی‌های عمومی واقعی (گروه ج) | **اثبات‌نشده — نیازمند درخواست جدا/مجوز بعدی** | در این چرخه مجاز نیست (D24)؛ استفادهٔ خواندنی از pool خصوصی و آزمون خروجی عمومی درخواست جدا می‌خواهد |
| اجرای runtime روی arm64/riscv64/ppc64le و نصب واقعی + سرویس تحت RPM | **در این محیط قابل‌آزمون نیست** | سه معماری فقط cross-build شده‌اند؛ payload `x86_64` یک RPM نصب و اجرا شد (بدون systemd)، ولی نصب واقعی + سرویس تحت RPM و اجرای runtime سه معماری به محیط/مجوز جدا نیاز دارد (`.github/workflows/ci.yml:147-173,199,239-250`) |

**تفکیک محدودیت‌ها (بازبینی و تصمیم کاربر، ۲۰۲۶-۱۱-۲۰):** سه حالت زیر از هم جدا می‌شوند و فقط ردیف (ب) «کار باز v1» است.

**الف) خارج از v1 — پشتیبانی نمی‌شود (نه «آزمون‌نشده»):**
- **REALITY/ECH:** importer امنیت reality را `unsupported`/`ReasonRealityUnverified` برمی‌گرداند (`internal/imports/record.go:178-179`) و engine با پیام «REALITY is outside v1» رد می‌کند (`internal/engine/config.go:284-289`)؛ peer محلی فقط بلوک TLS ساده می‌سازد (`internal/acceptance/exit_test.go:98-113`).
- **mKCP:** در allowlist ترنسپورت نیست (`internal/imports/record.go:170-171`، `internal/engine/config.go:239-240`)؛ `kcp` نگاشت نمی‌شود (`record.go:283-288`) و peer generator برای آن panic می‌کند (`exit_test.go:73-97`)؛ UDP/QUIC هم سیاستاً ممنوع است (`docs/requirements.md:98`، `docs/architecture.md:60`).
- **HTTPUpgrade:** همان حذف از allowlist (`record.go:170-171,283-288`)؛ سلول peer ندارد.
- **XHTTP روی h3/QUIC:** محصول ALPN دلخواه را می‌پذیرد (`internal/engine/config.go:238-241,330-332,403-425`) و ALPN peer پارامتری است (`exit_test.go:88-94,108-112`)، پس فقط یک سلول fixture کم است؛ ولی UDP/QUIC سیاستاً خارج از v1 است (`docs/requirements.md:98`، `docs/architecture.md:60`) و اجراشده فقط h2 بوده (`internal/acceptance/transports_test.go:37`). بنابراین policy-excluded است، نه untested.
- **mux/XUDP:** عمداً حذف شده (`internal/imports/importer.go:80,91`، `internal/imports/sanitize.go:164-166`) و در قرارداد sidecar فیلدی ندارد (`internal/contract/sidecar.go:7-17`، `internal/singbox/config.go:62-70`).
- **Windows، UDP/TUN، Android، GUI:** خارج از v1 با تصمیم کاربر (D24)؛ نیازمند milestone و تصمیم تازه‌اند.

**ب) پیاده‌سازی‌شده — وضعیت اجرا و کار باز v1 (به ترتیب):**
1. **reuse طولانی‌مدت ترنسپورت — انجام شد (۲۰۲۶-۱۱-۲۱):** سلول گیت‌شدهٔ `RL_ACCEPT_LONG_REUSE_TEST=1` در `internal/acceptance/reuse_test.go` یک تانل CONNECT روی WS/TLS (همان سلول اثبات‌شدهٔ R6) را باز می‌کند و سه درخواست keep-alive با بدنهٔ یکتا روی همان تانل می‌فرستد؛ پاسخ هر سه بایت‌به‌بایت برابر echo مبدأ و هر تلاش دقیقاً **۱ درخواست مقصد** (بدون تکرار/replay). **تعریف «طولانی‌مدت» از این پس همین پارامترهای تست است: ۳ درخواست، گسترهٔ زمانی ≥۳۰s و شکاف بی‌کار ≥۱۰s بین دو درخواست.** مشاهده: گسترهٔ ۳۲٫۰s، شکاف بی‌کار ۳۰٫۰s، **۱ اتصال خروجیِ مشتری** که در تمام پنجره زنده ماند و هر سه درخواست از آن رفت (۱ اتصال مقصد، بدون تحویل direct چون نام fixture روی host resolve نمی‌شود، بدون 502/timeout، سرویس پس از شکاف پاسخ‌گو). اولین probe سلامتِ زمان‌بندی‌شده (بررسی tick ۳۰s؛ چرخهٔ بعدی ۱۰ دقیقه) یک اتصال کوتاه‌عمر اضافی به exit ساخت (۰ تماس مقصد) و بسته شد؛ زمانِ آن اندازه‌گیری و به همان tick نسبت داده شد، و شمارش «برای این مشتری» از آن جدا شد و در PASS گزارش می‌شود. **شواهد (درخت کاری این تغییر، والد `ccdb4df`):** `gofmt -l internal/acceptance/reuse_test.go` خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./internal/acceptance/` با گیت خاموش سبز (۱۱۷٫۷s)؛ اجرای گیت‌شده **PASS در ۳۲٫۶۳s** (`RL_ACCEPT_LONG_REUSE_TEST=1 go test -count=1 -v -run TestTransportLongReuse ./internal/acceptance/`). commit محلی این تغییر: `test(acceptance): add gated long-duration transport reuse cell`. **قدم بعدی:** corrupt-state آفلاین زیر systemd که بعداً انجام شد (مورد ۵) و سپس session طولانی hysteria2.
2. **hysteria2 obfs — انجام شد (۲۰۲۶-۱۱-۲۱):** سلول گیت‌شدهٔ `RL_ACCEPT_HYSTERIA2_OBFS_TEST=1` در `internal/acceptance/r11_obfs_test.go` هر دو نوع مجاز را از مسیر واقعی محصول می‌آزماید: import لینک `hysteria2://…?obfs=salamander|gecko&obfs-password=…` از CLI، تولید بلوک top-level `obfs` در کانفیگ sidecar، و سپس CONNECT تأییدشده به مقصد fixture. سرور fixture همان باینری رسمی pinشدهٔ v1.14.4 است (archive sha256 `699a1b51…c24968`، commit `c005f353`) و بلوک `obfs` را کنار `users`/`tls` روی inbound hysteria2 می‌گذارد؛ این شکل پیش از اجرا با `sing-box check` همان باینری تأیید شد (هر دو نوع پذیرفته؛ `gecko` بدون رمز با `missing obfs password` رد می‌شود). **مشاهده:** هر دو حالت PASS — salamander **۱٫۳۵s** و gecko **۱٫۳۰s** (کل تست **۵۰٫۲۲s** با احتساب دانلود/بررسی digest؛ پکیج **۵۱٫۳۹s**)؛ هر کدام echo بایتیک‌سان، **دقیقاً ۱ تماس مقصد** و ۱ proof تأییدشده. دو حالت منفی (رمز اشتباه، و نوع اشتباه gecko در برابر salamander) هر کدام در **۱۱٫۴s** با **HTTP 504** و بدون payload، **۰ تماس مقصد**، و سرویس پس از شکست بالا و خوانا (`mode=targeted`، ۱ ردیف pool) — PASS. سلول فقط دو شکل fail-closed را می‌پذیرد: pool-timeout با `504 deadline exceeded` یا handoff خالی `200`؛ یک `502 dns failure` (که direct fallback نامحلول تولید می‌کند) یا هر پاسخ دیگر fail می‌شود. **شواهد:** `gofmt -l` روی دو فایل تغییرکرده خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./internal/acceptance/` با گیت خاموش سبز (**۱۲۲٫۰۴۷s**)؛ اجرای گیت‌شدهٔ بالا PASS. بدون تغییر رفتار محصول؛ تغییر harness فقط: پارامتر obfs اختیاری در helper سرور (`r11StartServer` → `r11StartServerWithObfs`) و جایگزینی بررسی no-op لایوینس با انتظار واقعی روی خروج پروسهٔ سرور. **یافتهٔ خارج از دامنه (اکنون رفع شد):** محصول رمز obfs را فقط برای salamander اجباری می‌کرد (`internal/imports/record.go:155-158`، `internal/engine/config.go:367-375`، `internal/singbox/config.go:232-240`) در حالی که `sing-box 1.14.4` برای gecko با رمز خالی هم `missing obfs password` می‌دهد؛ ورودی gecko بدون `obfs-password` در import پذیرفته می‌شد و کانفیگ تولیدشده را sidecar بارگذاری نمی‌کرد. طبق رفع ۲۰۲۶-۱۱-۲۱ در ابتدای «وضعیت فعلی» (دلیل `missing_obfs_password`)، import اکنون آن را رد می‌کند؛ برای رکورد قدیمی هم رفع ۲۰۲۶-۱۱-۲۱ در «تصمیم‌های باز» وضعیت مشتق‌شدهٔ `unsupported`/`missing_obfs_password` و کنارگذاری per-record از کانفیگ sidecar را اضافه کرد (state بازنویسی نمی‌شود) و validator سازندهٔ کانفیگ gecko بدون رمز را رد می‌کند. **قدم بعدی:** session طولانی hysteria2 (مورد ۳).
3. **session طولانی hysteria2:** آزمون‌نشده ولی شدنی است؛ به یک origin/exit محلی با عمر طولانی نیاز دارد.
4. **sidecar بستهٔ منتشرشده به‌جای باینری آزمون `/tmp` — انجام شد (۲۰۲۶-۱۱-۲۱):** `routelatch-v0.1.2-linux-amd64.tar.gz` و `SHA256SUMS` دوباره از ریلیز عمومی v0.1.2 دانلود شدند و فقط در `/tmp/opencode/rl-sidecar-proof` ماندند؛ چیزی نصب یا جایگزین نشد. sha256 فایل `SHA256SUMS` برابر `83b51b5b4f4b4d963b259de26c292963fb22276237df32908bc16ab1ea503bf2` (منطبق با digest اعلام‌شدهٔ همان دارایی در GitHub API) و sha256 تاربال برابر `fd6158bb7bb8973a9350e8419f6826066c276673c539e28ad7388d24ecb5de6e` (منطبق با `SHA256SUMS` و API؛ `sha256sum -c` = OK). محتویات: `bin/routelatch` با sha256 `5657903cc125c253756164afb8d8a0ebeae9e2cf6c98b0cd4e40f106e4363954` و `version` برابر `v0.1.2`/`88d04b1`، و `bin/sing-box` با sha256 `1264361b5f8868c8768f7555d3e77e20118bf503c41ac08bb290a0d99056aa3a` که **بایت‌به‌بایت** با باینری استخراج‌شده از آرشیو رسمی pin‌شدهٔ sing-box `v1.14.4` (archive sha256 `699a1b510aa4396b2176d32d1181920399ebf1e267835da6f3b598d0f7c24968`، commit `c005f353`) یکسان است؛ هر دو `1.14.4`/همان commit را گزارش می‌کنند و `SINGBOX-INFO` داخل بسته همین sha256 را تأیید می‌کند. سلول‌های گیت‌شده با همین sidecar اجرا شدند: `RL_ACCEPT_HYSTERIA2_TEST=1 RL_ACCEPT_HYSTERIA2_OBFS_TEST=1 RL_ACCEPT_SINGBOX_EXEC=<extracted>/bin/sing-box go test -count=1 -v -run 'TestR11Hysteria2RealTraffic|TestR11Hysteria2ObfsRealTraffic' ./internal/acceptance/` → **PASS**. `TestR11Hysteria2RealTraffic` **۴٫۶۱s**: echo بایت‌به‌بایت، **۱ تماس مقصد**، ۱ proof، exit مرده **502 با ۰ تماس جدید** (بدون direct)، مقصد تأییدنشده direct، D21، و بدون sidecar **502** — همه PASS. `TestR11Hysteria2ObfsRealTraffic` **۲۵٫۵۶s**: salamander **۱٫۳۳s** و gecko **۱٫۳۶s** هر کدام echo یکسان و **۱ تماس**؛ رمز اشتباه **۱۱٫۴۰s** و نوع اشتباه **۱۱٫۳۱s** هر کدام **HTTP 504 (deadline exceeded)** با **۰ تماس** و سرویس سالم — همه PASS؛ پکیج **۳۱٫۵۹s**. محصولِ تحت آزمون از همین درخت بازبینی‌شده ساخته شد و فقط `bin/sing-box` از تاربال آمد؛ از `bin/routelatch` تنها `version` خوانده شد. بدون تغییر کد محصول یا هارنس. **باقی:** فقط `amd64` آزموده شد (سه معماری دیگر و بسته‌های `.deb`/RPM نه)؛ session طولانی hysteria2 همچنان باز است (مورد ۳).
5. **corrupt-state زیر systemd — انجام شد (۲۰۲۶-۱۱-۲۱):** سلول گیت‌شدهٔ `TestA13SystemdCorruptState` در `internal/acceptance/a13_corrupt_state_test.go` (همان گیت `ROUTELATCH_SYSTEMD_TEST=1`) unit واقعی `deploy/systemd/routelatch.service` را با `--data-dir` و دو پورت تصادفی ایزوله اجرا می‌کند. ابتدا با state سالم بالا می‌آید، یک rule از طریق سرویس ثبت می‌کند، تمیز stop می‌شود و `backup` آفلاین می‌گیرد؛ سپس سه سناریو: JSON بریده (`{"schema_version":1,"mode":`)، JSON معتبر با `schema_version: 99`، و state غیرقابل‌خواندن (`chmod 000`). **مشاهده در هر سه:** unit به `failed` می‌رود با **`ExecMainStatus=10`** و **`NRestarts=0`** و `MainPID=0` (پس `RestartPreventExitStatus=10` از حلقهٔ restart جلوگیری می‌کند)؛ هیچ‌کدام از دو listener ترافیک bind نمی‌شود (پس هیچ درخواستی مسیر direct نمی‌گیرد)؛ `routelatch status` آفلاین exit `3` می‌دهد (هیچ managementی ادعای موفقیت نمی‌کند)؛ `doctor` آفلاین exit `10` با `kind=state_corrupt`؛ فایل **بایت‌به‌بایت دست‌نخورده** می‌ماند (۲۷ بایت برای JSON بریده) و mode هم عوض نمی‌شود؛ از `state.json.bak` هم بازگردانی خودکار نمی‌شود. `systemctl status` دلیل مستند را نشان می‌دهد: «state file is not valid JSON; it was not changed»، «state was written by a newer RouteLatch; refusing to guess its structure»، «state file cannot be read». **بازیابی مستند:** `restore <backup path>` آفلاین exit `0` و unit دوباره `active` با هر دو listener و همان rule؛ برای mode 000، chmod صریح `600` (تنها اقدام مجاز، بدون تغییر خودکار) باعث `doctor` exit `0` و شروع موفق با فایل بایت‌به‌بایت یکسان شد. **شواهد (درخت کاری این تغییر، والد `cdb99bd`):** `gofmt -l` روی فایل تغییرکرده خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./internal/acceptance/` با گیت خاموش سبز؛ اجرای گیت‌شده **PASS در ۲٫۰۰s** (اجرای نهایی روی همین درخت). **بازگردانی ماشین اثبات شد:** unit موقت و دایرکتوری‌های موقت حذف شدند، هیچ listener/unit موقتی باقی نماند، و سرویس زندهٔ کاربر پیش و پس `active` با همان `MainPID=142325`، `ExecMainStatus=0` و `NRestarts=0` و همان دو listener بود؛ هیچ state/config/proxy/DNS آن تغییر نکرد. **باقی:** فقط همین host/amd64؛ سناریو با unit موقت آزموده شد نه نصب واقعی کاربر؛ این سلول `backup`/`restore` را فقط آفلاین و روی backup خودش پوشش می‌دهد و بازیابی از `.bak`/فایل دستی را وعده نمی‌دهد (طبق قرارداد).
6. **tick چرخش ۳۰ ثانیه‌ای:** اولین tick حالا در سلول گیت‌شدهٔ long-reuse مشاهده می‌شود (probe انتقالی ۳۰s، ۰ تماس مقصد)؛ چرخهٔ ۱۰ دقیقه‌ای و اعمال health همچنان در acceptance درایو نمی‌شوند.

**ج) اثبات‌نشده — محدود به محیط/مجوز (شامل اجراشده ولی محدود):**
- **گروه ج:** استفادهٔ خواندنی از pool خصوصی، مقاصد دلخواه، churn، خروجی‌های عمومی واقعی، UDP واقعی و wire-level replay. تصمیم کاربر (۲۰۲۶-۱۱-۲۰): در این چرخه مجاز نیست و «کار باز v1» شمرده نمی‌شود.
- **A04/A06 در مقیاس:** آزمون در مقیاس در این چرخه مجاز نیست.
- **A01 (محیط پاک):** پذیرفته‌نشده و به ماشین/VM تازه نیاز دارد.
- **اجرای runtime روی arm64/riscv64/ppc64le:** فقط cross-build شده‌اند.
- **نشست GNOME برای A11:** KDE اثبات شده، GNOME نه.
- **نصب واقعی + سرویس تحت RPM:** payload `x86_64` یک RPM در container یک‌بارمصرف نصب و اجرا شد (بخش اجرای payload RPM)، ولی systemd واقعی/شروع سرویس در container ممکن نبود و نصب روی host مجاز نیست؛ اجرای RPM روی arm64/سایر معماری‌ها هم آزمون‌نشده است.
- **QUIC roaming و رفتار با exit عمومی کند/ناپایدار:** QUIC roaming به تغییر واقعی مسیر/اینترفیس شبکه نیاز دارد و در repo بازتولیدپذیر نیست؛ گروه ج هم مجوز ندارد.

این موارد مجوز ضعیف‌کردن D-item نیستند.

### تصمیم‌های باز (پیاده‌سازی‌نشده یا تازه‌بسته‌شده — نیازمند تصمیم/مجوز)

1. **وضعیت مؤثر برای رکوردهای ذخیره‌شدهٔ gecko بدون رمز obfs — انجام شد (۲۰۲۶-۱۱-۲۱):** قاعدهٔ مشتق‌سازی R5 حالا این رکورد را هم می‌گیرد. `EffectiveStatus` (مالک واحد) یک رکورد `hysteria2` با `obfs = gecko` و رمز خالی را صرف‌نظر از status ذخیره‌شده (جز `invalid`) به `unsupported` با دلیل پایدار **`missing_obfs_password`** مشتق می‌کند — همان رشته‌ای که import در `15d2acb` برمی‌گرداند. شرط یک‌بار در `contract.Hysteria2ObfsPasswordMissing` تعریف شده و import، view، probe و سازندهٔ کانفیگ sidecar همگی از همان استفاده می‌کنند. `pool.Dialable` این رکورد را رد می‌کند (پس `dialableRecords` آن را به engine نمی‌دهد)، probe انتقالی آن را بدون dial رد می‌کند، و `engine.prepareSidecar` هم به‌عنوان defense-in-depth آن را پیش از تولید کانفیگ کنار می‌گذارد، بدون افزودن به `Excluded` تا state بازنویسی نشود؛ مسیر `AddExit` هم همین رکورد را پیش از commit/Sync با خطای `unsupported` رد می‌کند تا گروه سالم handoff خود را از دست ندهد. exitهای سالم hysteria2 (gecko با رمز و hysteria2 بدون obfs) دست‌نخورده می‌مانند؛ اگر فقط رکورد خراب باشد sidecar کانفیگی نمی‌سازد و مسیر مستند «هیچ exit قابل استفاده نیست» (`pool_unavailable`، بدون direct) برقرار است؛ `singbox.Generate` هنوز هر exit غیرقابل استفاده را رد می‌کند. **شواهد:** تست‌های `TestEffectiveStatusDerivesStoredGeckoWithoutObfsPassword` (pool)، `TestStoredGeckoWithoutObfsPasswordDoesNotBreakTheGroup` و `TestAddExitRejectsStoredGeckoWithoutObfsPassword` (engine با `singbox.Generate` واقعی)، `TestStoredGeckoWithoutObfsPasswordIsIsolated` و `TestOnlyStoredGeckoWithoutObfsPasswordFailsClosed` (control؛ شامل برابری بایت‌به‌بایت state)، `TestTransportProbeSkipsStoredGeckoWithoutObfsPassword` (probes)، و `TestGenerateValidation`/`TestGenerateAcceptsGeckoWithPassword` (singbox) پیش از رفع روی HEAD `c5d48b4` (با تنها shim ثابت/helper قرارداد) fail دادند و پس از رفع pass شدند. `gofmt -l` روی فایل‌های تغییرکرده خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./...` سبز (۱۷/۱۷ پکیج، acceptance **۱۱۷٫۲۶۱s**، گیت‌ها خاموش). **بازبینی مستقل:** دور اول `Ready with nits` (یادداشت کهنهٔ پلن، نبود گارد `AddExit`، کامنت قرارداد) و پس از رفع، دور تأیید **Ready**؛ تست‌های هدفمند و `-race` روی دو تست engine gecko سبز. **باقی:** اگر گروه hysteria2 پیش از این رفع یک‌بار به‌خاطر همین رکورد fail کرده باشد، رکوردهای سالم آن گروه در state با `unsupported`/`sidecar_unavailable` ذخیره شده‌اند و طبق قاعدهٔ «پروتکل پشتیبانی‌شده status ذخیره‌شده را نگه می‌دارد» تا refresh/import دوباره خودکار برنمی‌گردند؛ این تغییر عمداً state را بازنویسی یا مهاجرت نمی‌کند. commit محلی همین تغییر (پیام: `fix(pool): derive stored gecko without obfs password as unsupported`).
2. **مورد دیگری در این بررسی یافت نشد.** این ادعا فقط تا همین بررسی اعتبار دارد.

### نقطهٔ شروع جلسهٔ بعد

1. `AGENTS.md` و همین بخش را بخوان؛ `git status` و `git log --oneline -5` را با
   «HEAD هنگام ثبت» مقایسه کن و تغییر/دادهٔ کاربر را حفظ کن.
2. وضعیت (بازخوانی ۲۰۲۶-۱۱-۲۱؛ HEAD `15d2acb`، درخت پاک): `/usr/bin/routelatch` طبق آخرین بررسی ۲۰۲۶-۱۱-۲۰ برابر v0.1.2 / `88d04b1` و sidecar `/usr/bin/sing-box` 1.14.4 است؛ سرویس `enabled`/`active` با `ExecMainStatus=0`/`NRestarts=0` (آخرین خواندن). چرخهٔ مستندات آخر reuse طولانی‌مدت ترنسپورت، hysteria2 obfs، sidecar بستهٔ منتشرشده، corrupt-state زیر systemd، رفع `missing_obfs_password`، بهداشت مستندات/checker و اجرای payload RPM روی amd64 را بست؛ جزئیات در «وضعیت فعلی» و «تفکیک محدودیت‌ها». کارهای R1–R12 و ریلیزهای `v0.1.0`/`v0.1.1`/`v0.1.2` بسته‌اند و push به دو remote (`github`، `origin`/forgejo) انجام شده است. `dist/BUILD-INFO` محلی v0.1.0/`068e9c2` است، پس بستهٔ محلی `dist/` (v0.1.0/`068e9c2`) تأیید‌نشده می‌ماند؛ ولی artifact منتشرشدهٔ v0.1.2/amd64 جداگانه دانلود و با `SHA256SUMS` و digest رسمی تأیید شد (بخش ب/۴). RouteLatch به‌عنوان پروکسی دسکتاپ KDE تنظیم شده (R7) و `routelatch desktop proxy restore` مقدار قبلی را بازمی‌گرداند.
3. **تصمیم‌های کاربر (۲۰۲۶-۱۱-۲۰، D24):** ادامهٔ v1 شامل همه‌چیز جز گروه ج است. گروه ج
   (pool خصوصی در مقیاس واقعی، مقاصد دلخواه، churn، خروجی‌های عمومی واقعی، UDP واقعی و
   wire-level replay) در این چرخه مجاز نیست و «اثبات‌نشده — نیازمند درخواست جدا/مجوز
   بعدی» می‌ماند. Windows، UDP/TUN، Android و GUI «خارج از v1»اند و تصمیم/milestone
   تازه می‌خواهند. تنها اقدام میزبانِ مجاز این چرخه: corrupt-state آفلاین زیر unit موقت و
   ایزولهٔ `systemd --user` و سپس بازگردانی کامل ماشین (انجام شد — ب/۵؛ هیچ اقدام میزبان
   دیگری مجاز نیست).
4. **ترتیب کارهای باز v1:** (۱) session طولانی hysteria2؛ (۲) A01 روی ماشین تازه؛ (۳) GNOME برای A11؛ (۴) نصب واقعی + سرویس تحت RPM؛ (۵) اجرای runtime سه معماری — دو مورد آخر بدون مجوز/محیط جدا شروع نمی‌شوند. reuse طولانی‌مدت ترنسپورت (بخش ب/۱)، hysteria2 obfs (بخش ب/۲)، corrupt-state زیر systemd (بخش ب/۵)، تأیید sidecar بستهٔ منتشرشده (بخش ب/۴)، رفع `missing_obfs_password` و اجرای payload RPM روی amd64 بسته شدند. جزئیات و شاهدها در «تفکیک محدودیت‌ها» و بخش «تصمیم‌های باز».
5. برای هر تغییر: آزمون واحد + به‌روزرسانی harness (جریان کامل بخشی از تعریف «انجام‌شده»
   است، چون F1/F8 را فقط آن گرفت)، سپس بازبینی مستقل و commit محلی.
6. شواهد و اعداد را از `spike/*/FINDINGS.md` و `docs/contracts.md` بردار، نه از حافظه.
   اگر دروازه‌ای شکست، تصمیم وابسته را باز کن و تضمین D-item را ضعیف نکن.
7. پایان هر task: ردیف وضعیت، HEAD، checks واقعی و قدم بعدی را همین‌جا به‌روز کن.

### مسائل و حدود مجوز

- **تصمیم‌های کاربر ثبت‌شده (۲۰۲۶-۱۱-۱۸):** مسیر `/home/...` که در تاریخچهٔ `spike/` مانده
  با بازنویسی در یک کپی پاک می‌شود (تاریخچه‌ای را که product نگه می‌دارد بازنویسی نمی‌کنیم)؛
  بررسی رسمی نام/علامت فعلاً لازم نیست و نام حفظ می‌شود؛ ایمیل committer قابل‌انتشار است؛
  و push مجاز است. شاهد کامل در `docs/release-readiness.md`.
- **remoteها:** `origin` = forgejo `routelatch.git`، و دومین remote = GitHub
  `m-hosseinpour/routelatch.git`؛ **فقط GitHub عمومی است**.
- **مجوز/ممیزی:** D20 با Apache-2.0 بسته شد (R3)؛ source offer و بستهٔ انتشار در R8 فراهم
  شدند. D22 (نقطهٔ تعهد CONNECT) با موافقت کاربر ثبت شد.
- **مجاز بدون درخواست تازه:** build/test محلی، دریافت وابستگی عمومی، commit محلی،
  تغییر کد/مستندات پروژه.
- **نیازمند درخواست جدا:** نصب/فعال‌سازی سرویس (برای R7 انجام شد)، تغییر proxy/DNS/تنظیمات
  دسکتاپ، استفاده از اطلاعات اتصال خصوصی، آزمون خروجی‌های عمومی، tag/release، و
  دستکاری پروژه‌ها/دادهٔ قبلی. push به دو remote فوق مجاز است.
- **تصمیم‌های کاربر (۲۰۲۶-۱۱-۲۰):** ادامهٔ v1 بدون گروه ج؛ گروه ج (pool خصوصی در مقیاس
  واقعی، مقاصد دلخواه، churn، خروجی‌های عمومی واقعی، UDP واقعی، wire-level replay)
  «اثبات‌نشده — نیازمند درخواست جدا/مجوز بعدی» است و کار باز این چرخه نیست. Windows،
  UDP/TUN، Android و GUI «خارج از v1»اند. تنها اقدام میزبانِ مجاز این چرخه، آزمون
  corrupt-state به‌صورت آفلاین زیر unit موقت و ایزولهٔ `systemd --user` و سپس بازگردانی
  کامل ماشین بود؛ انجام شد (ب/۵) و مجوز مصرف شد. هیچ اقدام میزبان دیگری مجاز نیست.
