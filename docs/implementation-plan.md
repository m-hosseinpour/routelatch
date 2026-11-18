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

## Milestoneهای بعدی — هنوز implementation-ready نیستند

1. **Windows:** build واقعی، named-pipe/ACL مدیریت، SCM و proxy settings/restore؛
   chmod لینوکس کافی نیست. scope دقیق با کاربر بسته شود.
2. **UDP و TUN:** semantics جدید، DNS capture/split/FakeDNS در صورت نیاز، IPv6 و
   loop/permissions، crash recovery، per-app coverage و mode-switch session control.
   وجود TUN در Xray اثبات تکمیل این مرحله نیست.
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
| A14 | T7، تصمیم مجوز و ممیزی build در R3 بسته شد؛ source offer، بستهٔ انتشار و بازبینی ادعای امنیت/نام باقی است |

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

### وضعیت فعلی (۲۰۲۶-۱۱-۱۸؛ همهٔ بررسی‌های این بخش روی `5d328a6` اجرا شده‌اند —
هنگام شروع، `git log` را ببین)

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
- **شاهد پذیرش:** `go test -count=1 -v ./internal/acceptance/` روی همین HEAD →
  **۲۰۶ گزارش `pass(...)`، ۰ شکست** (~۱۱۴s). گزارش پکیج، `ok`.
  `go build ./...`، `go vet ./...` و `go test -count=1 ./...` سبزند و tree پاک است.
- **بازبینی مستقل:** قراردادها (دو دور)، هر واحد پیاده‌سازی، پذیرش، کل تغییر
  (`1416c32..HEAD`)، D21 و تغییر ادغام‌شدهٔ R5+R6 verdict مثبت گرفتند (آخری
  approve-with-findings). یافته‌های مادی (F1–F8 و یافتهٔ bypass وضعیت `cooling`/`healthy`)
  رفع و بازبینی شدند. هیچ یافتهٔ باز مسدودکننده‌ای ثبت نشده است.
- **تصمیم‌های کاربر ثبت‌شده:** دروازهٔ handshake (تأیید واقعی هر جا ممکن + پذیرش صریح
  VLESS/Trojan)، ماتریس amd64/arm64 با GNOME/KDE، و D21 (هیچ مقصدی در هیچ لاگ/فایل
  ثبت نشود؛ پیشنهادها فقط در حافظه).

### کارها، به ترتیب و با وضعیت مجوز

| # | کار | نتیجهٔ متناظر | مجوز لازم؟ |
| --- | --- | --- | --- |
| R1 | آزمون نصب واقعی: اجرای `run` زیر systemd **user** واقعی، lifecycle (start/stop/restart، login)، port collision واقعی، بازیابی بعد از crash | A13 و بخشی از A01 | **بله** — نصب/فعال‌سازی سرویس |
| R2 | تغییر و restore واقعی پروکسی دسکتاپ روی GNOME یا KDE و آزمون crash recovery | A11 | **بله** — تغییر تنظیم دسکتاپ |
| R3 | **انجام شد** (`7792cac`، اصلاحیهٔ notices/attribution `4e0dde6`): D20 با Apache-2.0 بسته شد؛ `LICENSE`/`NOTICE`/`THIRD-PARTY-NOTICES.md` و ممیزی ۶۸ ماژول و ۶ کامپوننت embedشده (amd64+arm64، `go version -m` هم‌خوان) اضافه شدند. صفر مجوز ناشناخته؛ MPL-2.0 فقط dependency بدون تغییر؛ LGPL-3.0 با استثنای لینک استاتیک و متن GPL-3.0؛ جزئیات و محدودیت‌ها در `docs/licensing.md`. **باقی A14:** source offer برای MPL، بستهٔ سورس/انتشار و بازبینی امنیت/نام — بیرون از این تصمیم | A14 (بخش مجوز/ممیزی) | انجام شد — تصمیم شما ثبت شد |
| R4 | آزمون محدود با خروجی عمومی (اختیاری): یک/چند exit عمومی واقعی برای ردیف‌های عمومی A04/A06 | A04/A06 (ردیف عمومی) | **بله** — ترافیک خروجی عمومی |
| R5 | **انجام شد** (`640f83c`, `5d328a6`): مسیر import هرگز `unknown` نمی‌سازد (http → `unsupported`/`http_exit_not_verified`، scheme ناشناس → `unsupported`/`protocol_unsupported`) و view/status حالا رکورد undialable را **صرف‌نظر از status ذخیره‌شده** جز `invalid` مشتق می‌کند. `pool list`، `status` و `doctor` از یک مالک واحد (`EffectiveStatus`) می‌خوانند، state بازنویسی نمی‌شود و رکورد پیش از هر dial/probe رد می‌شود. **Residual پذیرفته‌شده:** protocol تهی/صفر عمداً دست‌نخورده است؛ semantics پروتکل‌های پشتیبانی‌شده تغییر نکرده؛ tick چرخش ۳۰ ثانیه‌ای در acceptance درایو نمی‌شود؛ `SyncStatuses` پس از فرمان تغییردهنده ممکن است cooldown منقضی را به `unknown` نرمال کند (bookkeeping قبلی، نه نوشتن مشتق‌سازی) | A06/§5 (کیفیت گزارش) | نه |
| R6 | **انجام شد** (`a69ae1e`): WS/TLS، gRPC/TLS (`h2`) و XHTTP/TLS (`h2`) با exit pin‌شدهٔ محلی، rule، `pool select` و CONNECT به peer loopback واقعاً آزموده شدند (هر سلول دقیقاً ۱ درخواست مقصد و پاسخ بدون تغییر)؛ pin غلط ۰ داده به مقصد می‌فرستد و سرویس بالا می‌ماند. **هنوز آزمون‌نشده:** REALITY/ECH، mKCP، HTTPUpgrade، h3، mux/XUDP، و رفتار طولانی‌مدت/reuse یک ترنسپورت | T1/T4 دقت ادعا | نه |

**قاعدهٔ انتخاب:** هیچ کار محلیِ بدون مجوز باقی نمانده است؛ R3، R5 و R6 بسته شدند.
R1، R2 و R4 بدون مجوز صریح شروع نمی‌شوند و «T7 تمام‌شده» به معنی انتشار یا پذیرش
کامل نیست.

### آنچه پذیرفته/اثبات نشده است

| معیار | وضعیت | دلیل/حد شاهد |
| --- | --- | --- |
| A02، A03، A05، A06، A08، A12 | اثبات محلی | شمارندهٔ تماس مقصد/خروجی و assertion مستقیم در harness؛ A06 شامل مشتق‌سازی legacy و مسیر import هر دو قاعده |
| WS/gRPC/XHTTP روی TLS | اثبات محلی (R6) | peer loopback pin‌شده، ۱ درخواست مقصد، pin غلط با ۰ داده؛ reuse طولانی‌مدت و h3 آزمون‌نشده |
| A04، A07، A09، A10، A11، A13 | اثبات محلی (بخشی) | mode switch زنده+در جریان، rerank/cooldown، مجوز فایل/socket، CLI، launcher env، crash/restart با kill -9 |
| A01 | **پذیرفته‌نشده** | نیاز به ماشین/محیط پاک و نصب واقعی (R1) |
| A11 (تنظیم واقعی دسکتاپ) | **پذیرفته‌نشده** | فقط runner جعلی (R2) |
| A13 (systemd واقعی) | **پذیرفته‌نشده** | نصب/فعال‌سازی انجام نشده (R1) |
| A14 | **مجوز/ممیزی بسته (R3)** | Apache-2.0 و ممیزی ۶۸ ماژول انجام شد؛ source offer، بستهٔ release و بازبینی امنیت/نام هنوز باز است |
| خروجی عمومی، wire-level replay، UDP واقعی، arm64 اجرا | **قابل‌آزمون نیست** | خارج از loopback/مجوز؛ arm64 فقط cross-build شده |

**محدودیت‌های آزمون‌نشدهٔ دیگر که ادعا را محدود می‌کنند:** HTTP CONNECT exit، TLS-pin
failure بیرونی، REALITY/ECH، mKCP، HTTPUpgrade، XHTTP h3، mux/XUDP، reuse طولانی‌مدت
ترنسپورت، tick چرخش ۳۰ ثانیه‌ای (فقط در unit/code-path دیده شده) و رفتار با exit عمومی
کند/ناپایدار. اینها مجوز ضعیف‌کردن D-item نیستند.

### نقطهٔ شروع جلسهٔ بعد

1. `AGENTS.md` و همین بخش را بخوان؛ `git status` و `git log --oneline -5` را با
   «HEAD هنگام ثبت» مقایسه کن و تغییر/دادهٔ کاربر را حفظ کن.
2. هیچ کار محلیِ بدون مجوز باقی نمانده است. برای R1–R4 مجوز روشن بگیر و تا آن زمان
   هیچ نصب/تغییر دسکتاپ/خروجی عمومی/انتشاری را خودکار شروع نکن.
3. برای هر تغییر: آزمون واحد + به‌روزرسانی harness (جریان کامل بخشی از تعریف «انجام‌شده»
   است، چون F1/F8 را فقط آن گرفت)، سپس بازبینی مستقل و commit محلی.
4. شواهد و اعداد را از `spike/*/FINDINGS.md` و `docs/contracts.md` بردار، نه از حافظه.
   اگر دروازه‌ای شکست، تصمیم وابسته را باز کن و تضمین D-item را ضعیف نکن.
5. پایان هر task: ردیف وضعیت، HEAD، checks واقعی و قدم بعدی را همین‌جا به‌روز کن.

### مسائل و حدود مجوز

- **تصمیم‌های باز:** هیچ تصمیم مجوزی باز نیست؛ D20 با Apache-2.0 بسته شد (R3). A14 از نظر مجوز/ممیزی بسته است و source offer، بستهٔ انتشار و بازبینی امنیت/نام باقی است. انتشار همچنان مجوز جدا می‌خواهد.
- **مجاز بدون درخواست تازه:** build/test محلی، دریافت وابستگی عمومی، commit محلی،
  تغییر کد/مستندات پروژه.
- **نیازمند درخواست جدا:** نصب/فعال‌سازی سرویس، تغییر proxy/DNS/تنظیمات دسکتاپ،
  استفاده از اطلاعات اتصال خصوصی، آزمون خروجی‌های عمومی، remote/push/انتشار، و
  دست‌کاری پروژه‌ها/دادهٔ قبلی.
