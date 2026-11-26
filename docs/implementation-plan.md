# پلن پیاده‌سازی RouteLatch

نسخهٔ اولیهٔ پلن — ۲۰۲۶-۱۱-۱۷

**داشتن پلن به‌تنهایی مجوز پیاده‌سازی، نصب سرویس یا انتشار نیست.** محدودهٔ مجاز
هر جلسه از درخواست جاری کاربر و بخش [کارهای باز](#open-work) مشخص می‌شود؛ انتشار
یک batch واجد شرایط طبق سیاست `AGENTS.md` (مجوز ایستادهٔ ۲۰۲۶-۱۱-۲۶) مجاز است.

## نتیجه و مرز

نسخهٔ اول مطابق D01–D20 در [نیازمندی‌ها](requirements.md): بستهٔ خودکفای Linux،
Go/Xray، پروکسی TCP هدفمند، مدیریت pool و پیشنهادها، CLI/منوی ساده، استفادهٔ
اختیاری پروکسی دسکتاپ. Windows، Android، UDP و GUI milestones بعدی‌اند. TUN در
D29 (۲۰۲۶-۱۱-۲۶) به‌عنوان **پوشش** پس از v1 تعریف شده و R16 نام گرفته است؛
هیچ بخشی از آن در v1.1.0 وجود ندارد و بدون مجوز R16-A شروع نمی‌شود.

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

- **نتیجه:** A01/A09/A11/A13/A15 روی ماتریس منتخب Linux؛ A13/A15 در R13 (D26).
- **وابستگی:** T2–T5.
- **دامنهٔ پیشنهادی:** `cmd/routelatch/`, `internal/platform/`, `deploy/`, packaging و README.
- **کار:** `run --headless` foreground؛ `run` پیش‌فرض app/tray با supervisor child
  (D26)؛ XDG autostart؛ systemd user unit فقط opt-in با linger؛ wrapper مستقل، health
  identity و env cleanup؛ desktop proxy opt-in، حفظ مقدار قبلی، ownership-aware restore
  و offline recovery.
- **کار:** self-contained binary/package؛ version/upgrade policy pinned، private state
  directories، log rotation/retention، config migration و backup قابل بازگشت.
- **آزمون:** ماشین/محیط پاک بدون skill/websearch/runtime دستی (این آزمون را ما اجرا
  نکردیم؛ A01 در ۲۰۲۶-۱۱-۲۱ با شهادت کاربر بسته شد — ردیف A01 در «آنچه
  پذیرفته/اثبات نشده است»)؛ autostart/login، tray/child restart و service opt-in؛
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
  integrity) و نام/registry/trademark دوباره بررسی شود؛ push/release طبق سیاست
  `AGENTS.md` است و تغییر public repository/destination یا remote جدید مجوز جدا می‌خواهد.
- **دروازه:** reviewer مستقل باید کل coherent change را با guidance مناسب بررسی کند؛
  یافته‌های مادی رفع و effects بررسی شوند؛ verdict و محدودیت اجرای پلتفرم‌ها گزارش شود.

## Milestoneهای بعدی — خارج از v1 (تصمیم کاربر ۲۰۲۶-۱۱-۲۰)

این چهار مورد **کار باز v1 نیستند** و به تصمیم/مجوز تازه و یک milestone جدا نیاز دارند
(D24؛ «آزمون‌نشده» به‌معنای «در صف v1» نیست). TUN در D29 تصمیم‌گیری و به R16 تبدیل
شده است؛ بقیه همچنان تصمیم تازه می‌خواهند:

1. **Windows:** build واقعی، named-pipe/ACL مدیریت، SCM و proxy settings/restore؛
   chmod لینوکس کافی نیست. scope دقیق با کاربر بسته شود.
2. **UDP و TUN:** TUN در D29 (۲۰۲۶-۱۱-۲۶) به R16 تبدیل شد: پوشش دو-stack روی
   modeها، DNS مستقیم و UDP غیرDNS بلاک در فاز اول، helper یک‌باره و fail-closed.
   D30 مسیر بعدی را تعریف کرد: DNS-through-pool و UDP هر کدام یک task تحقیقی
   جدا (R16-I/J) دارند؛ DNS یک switch مستقل خواهد داشت. DNS capture/FakeDNS،
   per-app coverage و پیاده‌سازی UDP همچنان تصمیم/مرحلهٔ جدا می‌خواهند. وجود TUN در Xray اثبات تکمیل R16 نیست؛ R16-A دروازه است. سیاست
   فعلی v1 همچنان no-UDP/QUIC است (`requirements.md` بخش «امنیت و موارد خارج از
   نسخهٔ اول» و `architecture.md §۳`).
3. **Android:** `VpnService`/foreground/per-app/protect/socket، VPN coexistence limits،
   یک Go runtime در artifact، policy مشترک و GUI بومی یا چندسکویی پس از تصمیم.
4. **GUI:** مصرف management contract مشترک؛ انتخاب framework و سرمایه‌گذاری TUI
   خارج از نسخهٔ اول و نیازمند تصمیم جدید است. **R15 سطح کنترل tray است، نه GUI؛**
   آن milestone جدا باقی می‌ماند و تصمیم D28 دامنهٔ GUI را باز نمی‌کند.

R15 (سطح کنترل tray) یک capability پس از v1 است که در بخش
[کارهای باز](#open-work) تعریف شده و جزو این چهار milestone آینده نیست.

## پوشش پذیرش و recovery

| معیار | task و check یکپارچه |
| --- | --- |
| A01/A13/A15 | T6/R13، نصب روی artifact رسمی و lifecycle در T7؛ A01 در ۲۰۲۶-۱۱-۲۱ با شهادت کاربر بسته شد، بدون آزمون محیط پاک توسط ما؛ A13/A15 مدل D26 در R13 پیاده شد (F: Ready؛ محدودیت‌های واقعی در جدول «آنچه پذیرفته/اثبات نشده») |
| A02/A03/A04 | T1/T3، counters عدم bypass و mode races در T7 |
| A05 | T1 پیش‌نیاز feedback؛ T5/T7 پذیرش proposal/approval/disable/no replay |
| A06/A07 | T4، refresh failures و pool churn در T7 |
| A08/A12 | T1/T3، DNS/HTTP/stream/UDP boundaries در T7 |
| A09 | T2–T6، private management، perms، terminal/log/secret checks در T7 |
| A10/A11 | T5/T6، CLI/menu/launcher/desktop recovery در T7 |
| A16 | R15، سطح کنترل tray: نگاشت actionها، confirmation، pagination، حالت unavailable/stopped، sanitize/D21، restart و Quit/restore؛ check نهایی در R15-H |
| A17 | R16، پوشش TUN: helper/setup، dual-stack، TCP→router/pool، sniffing، DNS مستقیم/UDP بلاک، fail-closed، persistence، tray/icon، D21؛ check نهایی در R16-A/H و `contracts.md §۱۰.۸` |
| A14 | **مجوز/ممیزی/بسته (R3+R8):** Apache-2.0، ممیزی ۶۸ ماژول، بستهٔ release با checksum و source offer؛ طبق تصمیم کاربر بررسی رسمی نام لازم نیست، ایمیل committer قابل‌انتشار است، و push مجاز است — ./release-readiness.md |

DNS-through-pool و پوشش UDP (D30/R16-I/J) عمداً معیار A جدا ندارند، چون هنوز
تحقیق‌نشده‌اند؛ پس از verdict و تصمیم، acceptance/contract جدید نوشته می‌شود و
به A17 اضافه نمی‌شود.

دادهٔ dev ساخته‌شده برای بازبینی دستی حذف نشود. import قدیمی read-only؛ upgrade
از state قبلی backup خصوصی و schema version داشته باشد. rollback موتور و state
باید compatible باشد؛ حذف broad یا reset برای تست ممنوع است. credential در commit،
fixture، report یا سرویس بیرونی قرار نگیرد.

<a id="open-work"></a>

## کارهای باز و وضعیت ادامه

این بخش مرجع واحد ادامهٔ کار در جلسه‌های بعد است؛ وضعیت در `AGENTS.md` یا سند
جداگانه تکرار نشود. هنگام شروع/پایان task، وضعیت، شاهد check و قدم بعدی اینجا
به‌روز شوند. درخواست «کارهای باز را شروع کن» فقط taskهای رفع‌مانع‌شده را مجاز می‌کند؛
نصب سرویس، تغییر شبکه/دسکتاپ و انتشار به مقصد جدید/تغییر remote مجوز جدا می‌خواهند؛ انتشار batch واجد شرایط طبق سیاست `AGENTS.md` (مجوز ایستادهٔ ۲۰۲۶-۱۱-۲۶) بدون درخواست جدا مجاز است.

### R16 — پوشش TUN (تعریف ۲۰۲۶-۱۱-۲۶؛ R16-A/B انجام شدند؛ R16-C بعدی)

**وضعیت واحد (a): R16 در ۲۰۲۶-۱۱-۲۶ فقط تعریف شد؛ هیچ کد، تست، TUN، route،
سرویس، unit، tray icon یا بسته تغییر نکرد و هیچ فرمان میزبان اجرا نشد.**
HEAD هنگام تعریف `e9a0cc9` و working tree پاک بود. این milestone پس از v1 است،
v1.1.0 و R1–R15 را باز نمی‌کند و **مجوز پیاده‌سازی R16-A به بعد را نمی‌دهد**؛
شروع هر task به درخواست/مجوز همان جلسه نیاز دارد. تصمیم محصول D29، معیار A17،
طراحی در `docs/architecture.md §۹` و قرارداد در `docs/contracts.md §۱۰` ثبت
شدند. این سند فقط ترتیب و دروازه‌ها را نگه می‌دارد.

**به‌روزرسانی ۲۰۲۶-۱۱-۲۶ (شروع R16-A/B):** R16-A با مجوز صریح کاربر در یک
کانتینر Docker دورانداختنی با netns ایزوله شروع شد؛ TUN/route/unit موقت فقط
داخل همان کانتینر مجاز است و هیچ تغییر DNS/firewall/route/سرویس هاست مجاز نیست.
R16-B (مدل داده/قرارداد) طبق پلن به‌صورت موازی شروع شد. تا اعلام verdict هیچ
نتیجه‌ای پذیرفته نیست و C/D/F/G مجاز نیستند؛ وضعیت نهایی هر دو task با شاهد
checkها در همین بخش ثبت می‌شود.

**دامنه و تصمیم‌های بسته‌شده (D29):**

- TUN **لایهٔ پوشش** روی `targeted|all-proxy|all-direct` است، نه mode پنجم؛
  سیاست/فهرست تأییدشده/registry/D11/D13/D21 همان مدل فعلی می‌مانند.
- helper ممتاز **یک‌باره** با `sudo routelatch tun setup` نصب می‌شود؛ سپس
  `routelatch tun status|on|off` و toggle در tray. engine/tray با root اجرا
  نمی‌شوند و بسته هیچ unit ممتازی ندارد.
- مسیر داده: TUN دو-stack → Xray TUN inbound با sniffing → SOCKS5 لوپ‌بک
  engine → router/registry → direct/pool. سرویس TUN هیچ سیاست مستقلی ندارد.
- DNS سیستم تغییر نمی‌کند؛ plain DNS (53) داخل TUN دریافت و **مستقیم** فرستاده
  می‌شود. UDP/QUIC غیرDNS در فاز اول بلاک است. دامنه فقط برای TLS/HTTP قابل
  sniff است؛ ECH/غیرTLS فقط IP/CIDR.
- **DNS-through-pool و UDP بخش A17 نیستند (D30):** هر کدام یک task تحقیقی جدا
  (R16-I/R16-J) پس از H می‌گیرند؛ DNS یک switch مستقل و جدا از mode/سایر
  ترافیک خواهد داشت. تا verdict آن‌ها، DNS مستقیم و UDP بلاک می‌ماند.
- IPv6 هم capture می‌شود (dual-stack). fail-closed تا زنده‌بودن سرویس TUN؛
  خرابی/restart خود سرویس یک پنجرهٔ کوتاه fail-open دارد.
- انتخاب TUN ماندگار است و پس از restart/Login خودکار فعال می‌شود؛ نبود helper
  با warning و آیکن روشن دیده می‌شود. `stopped` TUN را می‌بندد و اجازهٔ فعال‌شدن
  نمی‌دهد.
- آیکن tray رنگ/حالت TUN active/error/desired-off را جدا نشان می‌دهد.

**مسیرهای پیشنهادی کد (پس از R16-A نهایی می‌شوند):**
`internal/tun/` (client/service/config/lifecycle)، `internal/contract/tun.go`،
`internal/control/ops_tun.go`، `cmd/routelatch/tun.go` و hidden
`__tun-service`، `internal/app/tray*.go`، `internal/trayui/{action,types,menu}.go`،
و لمس نقطه‌های egress در `internal/control/service.go`، `internal/probes/engine.go`،
`internal/imports/fetch.go`، `internal/engine/config.go` و `internal/singbox/config.go`.

#### R16-A — دروازهٔ امکان‌سنجی TUN جدا (اولین task؛ فقط با مجوز محیط)

**نتیجه:** ثابت کند معماری D29 با نسخهٔ pin‌شدهٔ Xray و systemd روی Linux
امکان‌پذیر است، بدون آنکه policy/registry به سرویس دوم منتقل شود. تا verdict
`Ready` هیچ کد محصول/normal-run تغییر نمی‌کند.

**دامنه:** یک نمونهٔ دورانداختنی (container/VM یا host مجاز) با data-dir و
پورت‌های اختصاصی؛ هیچ تغییر DNS سیستم، firewall، سرویس زنده، پروکسی دسکتاپ یا
شبکهٔ کاربر. مجوز ساخت TUN/route و نصب موقت unit در همان محیط صریح گرفته شود.
اگر محیط اجازه نداد، verdict `Not verified` ثبت شود؛ R16-B فقط می‌تواند مدل
داده را ادامه دهد، ولی C/D/F/G شروع نمی‌شوند.

**کار و شاهد مورد انتظار:**

1. یک systemd **system** unit موقت با `User=<uid>`،
   `AmbientCapabilities=CAP_NET_ADMIN`، `CapabilityBoundingSet=CAP_NET_ADMIN` و
   hardening اجرا شود؛ باینری از یک کپی root-owned اجرا شود و ثابت شود سرویس
   غیرroot می‌تواند TUN بسازد/route اضافه کند و socket `0600` با peer uid کار
   می‌کند. شکست capability/hardening → بازنگری مدل helper، نه root engine.
2. Xray TUN با `gateway` دو-stack، `autoSystemRoutingTable` نیمه‌ها،
   `autoOutboundsInterface=auto`، sniffing `http,tls` و SOCKS outbound به یک
   fixture لوپ‌بک اجرا شود. شاهد: link/routeها، TCP از یک برنامهٔ محلی جدا از
   engine، دامنهٔ sniff‌شده در SOCKS، IPv4/IPv6، و پاک‌شدن route/link در stop.
3. DNS/53 به `freedom` bind‌شده به interface فیزیکی و UDP غیر53 به blackhole
   route شود. شاهد: DNS پاسخ می‌دهد، QUIC/UDP مستقیم دیده نمی‌شود، DNS سیستم
   تغییر نکرده و `autoSystemDnsToGateway` خاموش است.
4. engine egress: direct/probe/fetch و embedded-Xray pool dial در حضور TUN به
   interface فیزیکی bind شوند و شمارندهٔ loop صفر بماند. sidecar hysteria2 با
   `route.default_interface` (یا فیلد معادل همان نسخه) آزموده شود؛ اگر نسخهٔ
   pin‌شده پشتیبانی نکند، exitهای hysteria2 در حالت TUN با دلیل امن
   `unsupported`/`sidecar_loop_risk` کنار گذاشته می‌شوند و هرگز loop نمی‌کنند.
5. fail-closed: kill engine، TUN/route بماند و TCP refuse/blackhole شود؛ restart
   engine بازیابی کند؛ restart سرویس با lease بازسازی کند؛ `tun off`/stop routeها
   را پاک کند؛ SIGKILL سرویس هیچ route/DNS دائمی باقی نگذارد. پنجرهٔ fail-open
   اندازه‌گیری و ثبت شود.
6. تصادم ثابت‌ها: `rl-tun0`، gateways `198.18.0.1/15` و `fc00::1/64` با
   interface/routeهای موجود، LAN معمول و یک VPN/TUN خارجی (در محیط مجاز)
   تصادم نداشته باشند؛ نام/آدرس نهایی pin شود و conflict روشن گزارش شود.
7. D21: logهای Xray/TUN هیچ مقصد/دامنه‌ای در هیچ سطحی چاپ نکنند؛ sniffing فقط
   در حافظه باشد. این با تست منفی canary بررسی شود.

**ثبت:** نسخه/commit، kernel/systemd/iproute2، commands، خروجی‌ها، محدودهٔ
مشاهده، fail-open window و موارد آزمون‌نشده. verdict یکی از `Ready`، `Blocked`
یا `Not verified` است؛ هیچ fallback بی‌صدا (root engine، TUN در engine، حذف
fail-closed) مجاز نیست.

**به‌روزرسانی ۲۰۲۶-۱۱-۲۷ (R16-A — انجام شد؛ verdict `Ready`):** دروازهٔ فنی در
دو کانتینر Docker دورانداختنی با netns ایزوله اجرا شد (Ubuntu 26.04، kernel
7.0.0-44، systemd 259.9، iproute2 6.19.0، Docker 29.10.1)؛ هیچ تغییر DNS/
firewall/route/interface/سرویس هاست انجام نشد. شاهد: `spike/tun/FINDINGS.md` و
`spike/tun/evidence/`؛ بازتولید با `spike/tun/run.sh`.
- **runtime-verified:** unit system با uid غیرroot 1500 و فقط `CAP_NET_ADMIN`،
  باینری root-owned 0755، ساخت TUN/route، socket `0600` با peer-uid (root رد
  شد)؛ Xray `26.11.9` با `rl-tun0` دو-stack، routeهای نیمه، sniffing TLS/HTTP و
  IPv6 تا SOCKS لوپ‌بک؛ DNS/53 مستقیم از eth0 و UDP غیر53 blackhole؛
  `autoSystemDnsToGateway` خاموش و DNS سیستم بدون تغییر؛ direct و embedded-Xray
  pool dial با bind به eth0 و loop counter صفر (کنترلِ بدون controller یک بار
  loop شد)؛ sing-box 1.14.4 `route.default_interface` و hysteria2 هر دو پشتیبانی
  شدند (نیازی به exclude نبود)؛ engine kill → accept-then-EOF در ۱۲۰۵ms بدون
  direct؛ restart engine/SOCKS بازیابی کرد؛ restart سرویس با lease در ~۲۷ms؛
  SIGKILL fail-open ۱۱۷۰ms (۲۲ms teardown + ۱۱۴۷ms systemd restart)؛ `tun off`
  پاک‌سازی ۲۱ms؛ پس از SIGKILL هیچ route/link/DNS یتیم نماند؛ ثابت‌ها با
  routeهای واقعی هاست (read-only) و LAN/VPN شبیه‌سازی‌شده تصادم نداشتند.
- **constraint 1 (D21):** سرویس باید `logLevel: "none"` و `access: "none"` را
  ثابت نگه دارد و هیچ knob سطح لاگ نداشته باشد؛ stock Xray در info/debug مقصد را
  لاگ می‌کند (`proxy/tun/handler.go:246`). canary در تنظیمات shipped صفر بود؛
  تضمین «حتی debug» با Xray دست‌نخورده برآورده نمی‌شود و یک debug آینده نیازمند
  patch/redaction و تصمیم جدید است. این محدودیت در `contracts.md §۱۰.۵` ثبت شد.
- **constraint 2:** مسیر DNS با `autoOutboundsInterface=auto` آزموده و پذیرفته شد
  (معادل مجاز §۱۰.۵)؛ bind صریح `sockopt.interface` روی سرویس غیرroot timeout داد
  و استفاده نمی‌شود.
- **untested:** ترافیک عمومی/off-link، تضمین literal debug D21، bind صریح DNS،
  sidecar زیر unit hardened، R-28/R-30 (تغییر interface/هم‌زمانی VPN کامل)،
  per-app/ECH/QUIC غیر از blackhole. این‌ها C/D/H یا تصمیم جدا هستند.
**next:** C/D/F/G با Ready شدن A باز شدند؛ C با B موجود شروع می‌شود و B اکنون
وابستگی برآورده‌شده دارد.

#### R16-B — قرارداد، state/status و opها

**وابستگی:** R16-A `Ready` برای تصمیم‌های فنی؛ ولی مدل داده می‌تواند موازی
شروع شود. **نتیجه:** D29/A17/§۱۰ به تایپ‌ها، opها و تست‌های قراردادی تبدیل شوند،
بدون تغییر رفتار فعلی.

- `internal/contract/tun.go` (یا معادل): `TunState`، `TunStatus`، ثابت‌های
  `off|starting|on|stopping|error|unavailable`، دلیل‌های امن، فیلد `TunEnabled`
  در `Config` (additive، پیش‌فرض false).
- `internal/control/protocol.go`: `OpTunStatus/OpTunEnable/OpTunDisable`؛
  `StatusData.tun`؛ `warnings` جدید؛ بدون ErrorKind تازه.
- `internal/control/ops_tun.go`: اعتبارسنجی mode غیر stopped، helper/platform،
  state_locked/tun_conflict، idempotency؛ `tun disable` همیشه مجاز؛ `mode stopped`
  اول disable.
- تست‌های قراردادی: JSON قدیمی بدون `tun` → nil/false و سازگاری؛ schema version
  ۱ بدون migration؛ خطاهای stopped/unsupported/state_locked؛ صفر مقصد در payload.
- `docs/contracts.md §۱۰` و `requirements.md D29/A17` مرجع‌اند؛ تغییر نام/فیلد
  نیازمند به‌روزرسانی همان‌ها است.

**check:** `go test ./internal/contract/... ./internal/control/...` واحدهای
جدید/مرتبط سبز؛ `go vet ./...`؛ decode payload قدیمی و جدید؛ هیچ فیلد مقصدی در
status نیست.

**به‌روزرسانی ۲۰۲۶-۱۱-۲۷ (R16-B — انجام شد):** مدل داده/قرارداد TUN اضافه شد:
`internal/contract/tun.go` با `TunState`/`TunStatus`/دلیل‌ها/هشدارها،
`Config.TunEnabled` (`tun_enabled`، additive، schema 1)، `StatusData.Tun`،
`OpTunStatus/Enable/Disable` و `internal/control/ops_tun.go` با seam صادق
«unavailable»، idempotency و اعتبارسنجی mode/helper/platform/state_locked؛
`mode stopped` فقط با گزارش مثبت `off` از disable رد می‌شود و در `unavailable`
fail-closed می‌بندد. `dns`/`udp` به `direct`/`blocked` pin و `detail` sanitize
شدند؛ ErrorKind/exit تازه ساخته نشد. مستندات §۴.۲/§۷.۱/§۱۰ به‌روز شدند.
**checkها:** `gofmt -l` خالی؛ `go vet ./...` و `go build ./...` سبز؛
`go test -count=1 ./internal/contract/... ./internal/control/...` و full
`go test -count=1 ./...` سبز (acceptance ۱۱۵s)؛ `-race` contract/control سبز.
بازبینی مستقل **Ready with findings** بود؛ پنج یافتهٔ جزئی رفع و با تأیید
متمرکز همان reviewer بسته شدند. **معوق تا C/D:** restore-to-stopped و
cross-validation نوع/دلیل خطا؛ helper در حالت خطا به‌صورت محافظه‌کارانه
`missing` (unverified) گزارش می‌شود. **next:** C/D/F/G تا verdict `Ready`
شدن R16-A قفل‌اند؛ runtime واقعی TUN هنوز وجود ندارد.

#### R16-C — سرویس TUN (`internal/tun`, hidden `__tun-service`)

**وابستگی:** R16-A و R16-B. **نتیجه:** سرویس جدا که TUN/routes را می‌سازد،
TCP را به SOCKS لوپ‌بک می‌دهد، DNS/53 را مستقیم و UDP غیرDNS را بلاک می‌کند،
بدون هیچ سیاست مقصدی.

- config ثابت §۱۰.۵: `rl-tun0`, MTU، gatewayهای تأییدشده، routeهای نیمه،
  sniffing، routing به `rl-tun-socks`/`rl-tun-dns`/`rl-block`؛ log error/access
  خاموش.
- lifecycle: start core تازه در هر enable؛ stop کامل در disable؛ lease
  `/run/routelatch-tun/lease.json` برای restart همان boot؛ `/run` در boot پاک
  می‌شود و auto-enable قبل از login وجود ندارد؛ core در process restart نمی‌شود.
- control socket `0600` + peer uid + فقط `enable|disable|status` + listener
  لوپ‌بک؛ هیچ arg دلخواه.
- stop ترتیب TUN→core→route/link؛ خطای پاک‌سازی گزارش شود؛ orphan route/link
  تست منفی.
- `internal/singbox/config.go`: در حالت TUN `route.default_interface` معادل
  نسخهٔ pin‌شده اضافه شود (پس از تأیید R16-A) و از import پذیرفته نشود.
- tests: fake core/control برای lifecycle و config؛ gated TUN integration با
  fixture لوپ‌بک؛ بدون تغییر شبکهٔ پیش‌فرض در `go test ./...`.

**check:** unit tests + gated `RL_TUN_TEST=1` روی محیط مجاز؛ route/link cleanup؛
no destination log؛ dual-stack.

#### R16-D — یکپارچگی engine: enable/disable، ماندگاری و bypass

**وابستگی:** R16-B/C. **نتیجه:** engine config/status/ops و مدل اجرا TUN را
به‌عنوان پوشش می‌شناسد و dialهای خودش loop نمی‌کنند.

- `Config.TunEnabled` از CLI/tray؛ auto-enable پس از start/restart/Login اگر
  helper سالم است؛ نبود helper → warning و TUN off، بدون fail کل proxy.
- شروع: اگر desired و mode غیر stopped، engine اول interface فیزیکی/bypass
  خودش را تنظیم می‌کند، سپس سرویس TUN را پیش از listenerها با آدرس SOCKS
  پیکربندی‌شده بالا می‌آورد (پنجرهٔ blackhole کوتاه)؛ شکست TUN مانع listener
  readiness نمی‌شود ولی status/tray error می‌دهد. `status.ready` فقط
  listener-based می‌ماند. اگر mode stopped و TUN از crash فعال است، اول disable
  و سپس مدیریت عادی؛ شکست disable خطای روشن است. اگر bind listener پس از
  فعال‌شدن TUN شکست بخورد، engine باید TUN را disable کند؛ در غیر این صورت
  supervisor/CLI مسیر recovery را نشان می‌دهد.
- stop/Quit/uninstall: اول disable TUN؛ شکست → عملیات متوقف شود؛ `mode stopped`
  TUN را می‌بندد و در stopped اجازهٔ enable نمی‌دهد؛ preference فقط با `tun off`
  پاک می‌شود؛ mode switch TUN را restart نمی‌کند و sweep معمولی D13 را اجرا
  می‌کند؛ تغییر آدرس SOCKS فقط با disable/re-enable.
- egress bypass مشترک: direct/probe/fetch/resolver و embedded-Xray pool dial
  به interface فیزیکی؛ loopback مستثنا؛ kernel <5.7 → `unsupported`. یک
  controller یک‌بار در start ثبت شود و فقط در TUN active bind کند؛ بعد از
  disable روی dial عادی اثر نداشته باشد.
- restart engine: TUN سرویس/route می‌ماند، اتصال‌ها fail-closed؛ engine جدید
  adopt/disable می‌کند؛ supervisor restart طبق R13 دست‌نخورده.
- doctor/status: helper installed/missing/incompatible، service error، DNS
  direct/UDP blocked، conflict، desktop-proxy overlap، all-direct warning؛
  target SOCKS کهنه → disable/re-enable، نه تغییر بی‌صدا؛ بدون مقصد.

**check:** unit/fake برای enable/stop/quit/restart/persistence؛ gated TUN
end-to-end با loopback pool/direct/probe/refresh؛ شمارندهٔ loop=0؛ no orphan
TUN پس از Quit/SIGTERM.

#### R16-E — CLI و Terminal menu

**وابستگی:** R16-B/D. **نتیجه:** `routelatch tun status|on|off` با JSON/exit
مستند و `tun setup|uninstall` privileged/CLI-only.

- `cmd/routelatch/tun.go` + `execute.go`/usage؛ `tun status|on|off` به opها؛
  `tun setup|uninstall` با sudo، idempotent، ownership marker، refusal unit
  غیرمالک؛ پیام‌های روشن برای helper/kernel/systemd.
- `cmd/routelatch/menu.go`: آیتم TUN coverage با status/on/off/help؛ نصب helper
  فقط نمایش دستور `sudo routelatch tun setup` (D28).
- تست CLI: exit codeها، JSON additive، stopped/unsupported/state_locked،
  idempotency setup با fake systemctl/paths، بدون لمس manager واقعی.
- README/`deploy/README.md` در R16-G به‌روزرسانی می‌شوند؛ در این task فقط
  command surface.

**check:** `go test ./cmd/routelatch/...`؛ run واقعی `--json` روی محیط مجاز؛
help/usage؛ no secret/destination.

#### R16-F — tray: Coverage menu و آیکن رنگ‌دار

**وابستگی:** R16-B/D. **نتیجه:** toggle/status TUN در tray و رنگ/حالت آیکن،
بدون منطق دوم.

- `internal/trayui/action.go`: `ActionTunEnable/ActionTunDisable` (یا toggle) و
  mapping به op؛ `allActions` و تست mapping؛ labelها sanitize.
- `internal/trayui/menu.go`: زیرمنوی Coverage با وضعیت on/off/starting/error،
  helper missing، DNS direct/UDP blocked warning؛ actionهای نامعتبر disabled.
- `internal/trayui/types.go`: `Status.Tun` از `StatusData`؛ snapshot/fingerprint.
- `internal/app/tray.go`: `trayStatusIcon` حالت‌های TUN active/error/desired-off
  با رنگ/علامت متمایز؛ تست تمایز رنگ‌ها و عدم تصادم؛ tooltip امن.
- رفتار تأیید: enable (تغییر پوشش سیستم) و disable (کاهش پوشش) سیاست مشخص و
  تست‌شده؛ بدون مقصد/URL در label/notification.
- GNOME/Wayland و tray hostهای دیگر همچنان محدودیت R-16 را دارند.

**check:** `go test ./internal/trayui/... ./internal/app/...`؛ gated واقعی KDE
روی محیط مجاز؛ اسکرین‌شات/رفتار icon و toggle.

#### R16-G — setup ممتاز، packaging و مستندات

**وابستگی:** R16-C/D/E. **نتیجه:** `tun setup` یک‌باره امن و قابل حذف؛ بسته
همچنان بدون unit/privileged payload؛ مستندات/بسته سازگار.

- `internal/tun/setup` (یا معادل): نوشتن unit/socket/config/root ownership،
  `systemctl daemon-reload/enable/start socket`، readiness، rollback؛
  `uninstall` فقط مالک را پاک می‌کند و اول disable. setup باید یک کپی
  root-owned از باینری در `/usr/libexec/routelatch/` بگذارد و unit همان را
  اجرا کند؛ هرگز باینری قابل‌نوشتن کاربر را با `CAP_NET_ADMIN` اجرا نکند
  (`BindReadOnlyPaths` روی فایل کاربر کافی نیست). `RuntimeDirectory` با
  مالکیت uid برای lease/socket لازم است.
- بررسی امنیتی: کپی root-owned باینری در `/usr/libexec/routelatch/`،
  permissionها، socket `0600`/peer uid، عدم پذیرش arg دلخواه، hardening unit
  (از جمله `/dev/net/tun`)، log بدون مقصد.
- packaging: `scripts/check-package.sh` همچنان «هیچ unit/postinst در بسته» را
  تأیید کند؛ `tun setup` تنها سازندهٔ unit است؛ README/packaging/README/
  deploy/README/CONTRIBUTING و docs/README به‌روزرسانی شوند؛ release notes
  فقط اگر batch منتشر شد.
- مستندات کاربر: پیش‌نیاز Linux ≥5.7/systemd/CAP_NET_ADMIN، DNS مستقیم، UDP
  بلاک، ECH، همهٔ کاربران host، fail-open window، و recovery روشن: `tun off`
  نیازمند engine است، ولی `sudo routelatch tun uninstall`/`systemctl stop
  routelatch-tun.service` مسیر مستقل است.
- migration/upgrade: config additive، بکاپ/restore، نبود unit قبلی؛ uninstall
  هیچ state/داده کاربر را حذف نکند.

**check:** `check-package.sh`, `check-artifact.sh` (بدون تغییر payload)؛
idempotent setup/uninstall با fake manager؛ gated نصب واقعی فقط روی محیط مجاز.

#### R16-H — پذیرش A17 و بازبینی نهایی

**وابستگی:** R16-A..G. **نتیجه:** A17 با شاهد اجرایی و review مستقل.

- سناریوی کامل: `tun setup` → `tun on` در targeted → app بدون proxy (TLS/HTTP/IP)
  → mode all-proxy/all-direct/stopped → DNS/53/UDP/QUIC → IPv6 → engine kill/restart
  → tray icon/menu → `tun off`/Quit → `tun uninstall`.
- fail-closed: kill engine، TUN/route باقی، TCP refuse؛ restart بازیابی؛
  restart سرویس/lease؛ SIGKILL و route cleanup؛ پنجرهٔ fail-open ثبت.
- leak/counter: pool empty/dead روی مقصد تأییدشده → خطا نه direct؛ localhost/LAN
  direct؛ DNS سیستم بدون تغییر؛ zero UDP leak؛ zero loop؛ zero destination log.
- persistence: restart app/service/Login؛ helper missing → warning نه silent.
- network change: تغییر interface پیش‌فرض/route (در محیط مجاز) رفتار مستند
  R-28 را نشان دهد؛ تا `tun off/on` هیچ loop/leak بی‌صدا نباشد.
- platform: فقط amd64/arm64 ادعا؛ kernel/systemd matrix مستند؛ Windows/Android
  خارج از دامنه.
- review مستقل کل change (C/D/F/G) با guidance مربوط؛ یافته‌های مادی رفع و
  effects دوباره بررسی؛ verdict/limits در همین بخش.

**check نهایی:** `go build ./...`، `go vet ./...`، `go test -count=1 ./...`،
acceptance پیش‌فرض offline، gated `RL_TUN_TEST=1` روی محیط مجاز، package
checks، و گزارش passed/failed/not-run. انتشار فقط طبق `AGENTS.md` و پس از
گیت‌های release.

#### R16-I — DNS-through-pool و switch مستقل (تحقیق؛ بعد از H)

**وابستگی:** R16-A برای واقعیت‌های TUN، و R16-H برای پذیرش فاز اول. این task
**بخشی از A17 نیست** و تا verdict آن، رفتار DNS مستقیم/UDP بلاک دست‌نخورده
است. تصمیم D30 مرجع است.

**نتیجه:** مشخص شود DNS از pool چطور و با چه تضمین‌هایی انجام می‌شود؛ یک
switch مستقل (`dns_via_pool` یا نام نهایی) فقط مسیر DNS را عوض کند، نه mode،
نه routing مقصد و نه پوشش TUN. اگر شواهد کافی نبود، verdict `Blocked`/`Not
verified` ثبت شود؛ پیاده‌سازی ناقص یا fallback مستقیم بی‌صدا مجاز نیست.

**پرسش‌های تحقیق (پیش از کد):**

- transport: DoH/DoT/DNS-over-TCP از طریق exit؛ کدام exitها پشتیبانی می‌کنند؛
  bootstrap resolver بدون وابستگی به DNS سیستم؛ loop با TUN؛ رفتار با IPv6.
- policy: همهٔ queryها یا فقط دامنه‌های تأییدشده؛ رابطه با targeted/all-proxy/
  all-direct؛ pool خالی/مرده → خطای روشن DNS، نه direct؛ cache/retry و سقف
  latency؛ DNS سیستم همچنان تغییر نکند.
- switch: کلید مستقل config + CLI (`config set` یا `tun dns ...`)، پیش‌فرض
  امن، رفتار در restart/backup، و اینکه tray فقط status/switch داشته باشد یا
  نه؛ عملیات متنی در CLI بماند (D28).
- D21: no query/domain in log/state/tray؛ exit فقط resolver را می‌بیند؛
  resolver عمومی/خصوصی و trust boundary مستند شود.
- سازگاری: DNS مستقیم فعلی، captive portal، resolver محلی، DoH مرورگر و
  hostname resolution برنامه‌ها؛ اگر switch خاموش است، رفتار فاز اول عیناً.

**پیاده‌سازی (فقط پس از verdict و تصمیم جدا):** DNS path در سرویس TUN/engine،
انتخاب exit و fail-closed، status/CLI/switch، تست‌های fixture و canary D21.

**check:** گزارش verdict، transport/exit matrix، تست DNS با pool سالم/مرده،
switch off/on، عدم تغییر DNS سیستم، no loop/leak، صفر مقصد در log/state.

#### R16-J — پوشش UDP/QUIC (تحقیق؛ بعد از H)

**وابستگی:** R16-A/H و D30. **بخشی از A17 نیست** و تا verdict، UDP غیرDNS
بلاک می‌ماند. این task فقط امکان‌سنجی و تصمیم است، نه اجازهٔ ضعیف‌کردن
سیاست no-UDP فعلی.

**پرسش‌های تحقیق:**

- کدام exitها UDP را واقعاً پشتیبانی می‌کنند (hysteria2، VLESS/Trojan/XUDP،
  SOCKS5 UDP) و با چه هزینه/نرخ شکست؛ comport با sidecar.
- TUN UDP flow، session/NAT، DNS/53، QUIC/HTTP3، IPv6، idle timeout، resource
  limits و teardown؛ loop با برنامه‌های UDP خود RouteLatch.
- policy: targeted/all-proxy/all-direct، approved domain بدون sniff، fail-closed
  در pool خراب، no direct leak، D21 (no flow log)، per-app/per-UID خارج از دامنه.
- switch/policy: UDP همراه TUN یا opt-in جدا؛ اثر بر no-UDP/QUIC v1 و
  A12/A17؛ نیاز به acceptance/decision جدید.

**check:** verdict با شواهد منبع/اجرا، matrix exitها، تست fixture UDP/QUIC در
صورت امکان، صفر leak/direct در pool خراب، و تصمیم صریح برای ادامه یا توقف.
اگر نتیجه مثبت بود، contract/acceptance جدید پیش از کد نوشته شود.

#### ترتیب، موازی‌کاری و دروازه‌ها

```text
R16-A (gate) ──► R16-B ──► R16-C ──┐
                    │              ├──► R16-D ──► R16-E ──► R16-F ──► R16-H
                    └──────────────┘                     └──► R16-G ──┘
                                                                     │
                                                    R16-I / R16-J (تحقیق، موازی) ◄┘
```

- R16-A تنها دروازهٔ فنی است؛ تا `Ready` نشدن، C/D/F/G شروع نشوند. B می‌تواند
  روی مدل داده موازی شود.
- C و D به‌ترتیب سرویس و engine را می‌سازند؛ E/F روی قرارداد B/D؛ G بعد از
  C/D/E. H آخرین task فاز اول است.
- R16-I/J پس از H شروع می‌شوند و **بخشی از A17 نیستند**؛ تحقیق آن‌ها می‌تواند
  از شواهد R16-A استفاده کند، ولی پیاده‌سازی فقط با verdict و تصمیم/قرارداد
  جدید. I و J مستقل و قابل موازی‌کاری‌اند.
- هیچ taskی مجوز نصب واقعی systemd، تغییر شبکه، تست با خروجی عمومی یا انتشار
  نیست؛ این‌ها در R16-A/G/H/I/J با مجوز همان جلسه/محیط انجام می‌شوند.
- اگر R16-A `Blocked` شد، تصمیم D29 باز می‌شود؛ کاهش بی‌صدا (root engine، TUN
  در engine، حذف fail-closed، direct کردن UDP) مجاز نیست.

### R15 — سطح کنترل tray (تعریف ۲۰۲۶-۱۱-۲۵؛ R15-0 تا R15-H انجام شدند؛ v1.1.0 در ۲۰۲۶-۱۱-۲۶ منتشر شد)

**وضعیت واحد (a): R15-0 تا R15-H انجام شدند و `v1.1.0` در ۲۰۲۶-۱۱-۲۶ منتشر شد؛ هیچ کار باز R15 نمانده است.** تصمیم D28 و معیار A16
ثبت شدند و طراحی در `docs/contracts.md §۷.۳`/`§۷.۵` و
`docs/architecture.md §۷` آمده است. در جلسهٔ تعریف (۲۰۲۶-۱۱-۲۵) هیچ کد، تست،
بسته، سرویس، autostart، تنظیم دسکتاپ یا tray واقعی تغییر نکرد. HEAD هنگام تعریف
`cff2884` و working tree پاک بود. commit تعریف این سند در پایان همان جلسه انجام
شد؛ self-review پلن علیه چک‌لیست coverage/grounding/sequence/verification انجام
شد و verdict برای کار رفع‌مانع **Ready for R15-A** بود؛
R15-B به نتیجهٔ دروازهٔ A وابسته بود. R15 یک milestone **پس از v1** است و
R1–R14/v1.0.1 را باز نمی‌کند؛ R14 بسته می‌ماند.

**انتشار v1.1.0 — انجام شد (۲۰۲۶-۱۱-۲۶):** تگ annotated `v1.1.0` (شیء
`7abe9704f66cbcc78fe6340093e8a365324b9407`) روی commit
`532c1697da6140fdfd5258dc98da2c1f7927cc61` (`532c169`) در هر دو remote push شد.
`CI` `45796357850` روی همان commit سبز بود و `Release` `45800238447` همهٔ jobها
(plan/build/package/RPM/assemble/publish) را سبز گذراند. Release عمومی `v1.1.0`
(id `459257636`، بدون draft/prerelease) با ۱۷ دارایی (چهار tarball، چهار
`.deb`، چهار spec، چهار RPM باینری و `SHA256SUMS`) منتشر شد:
<https://github.com/m-hosseinpour/routelatch/releases/tag/v1.1.0>. body با
«RouteLatch v1.1.0 — tray control surface» شروع می‌شود (پیام تگ annotated).
انتشار طبق مجوز ایستادهٔ ۲۰۲۶-۱۱-۲۶ در `AGENTS.md`/همین پلن انجام شد، بدون
درخواست جدا. جزئیات در `docs/release-readiness.md`.

**به‌روزرسانی ۲۰۲۶-۱۱-۲۵ (R15-A):** دروازهٔ فنی منو روی Plasma 6.6.7/X11 و
bus ایزوله اجرا شد؛ شاهد در `spike/tray/MENU-FINDINGS.md` و
`spike/tray/evidence/menu-*-2026-11-25.*` است. verdict: R15-A **انجام شد** و
R15-B0 **Ready** است؛ R15-D1 باید live apply را با key پایدار پیاده کند و
صفحه‌گردانی را برای جلوگیری از ۵۱ `GetLayout` در هر صفحه دسته‌بندی کند. شکاف‌های
ثبت‌شده: اسکرین‌شات checkmark/سطح سوم و آزمون Wayland/GNOME.

**به‌روزرسانی ۲۰۲۶-۱۱-۲۵ (R15-B0):** `status.resume_requires_restart` به‌صورت
فیلد additive در `StatusData` پیاده شد: فقط وقتی true که همین process وارد
stopped شده باشد (`s.stopped && s.stoppedByThisProcess`)؛ process تازه در
stopped مقدار false صریح دارد؛ payload قدیمی بدون فیلد در مشتری `nil`/unknown
می‌ماند و `schema_version` تغییر نکرده است. هشدار `status` بین stopped-by-process
و process تازه فرق می‌کند. `go vet`/`go build`/`go test ./internal/control/...`
سبز و full tests (acceptance ۱۳۰٫۱s) سبز؛ بازبینی مستقل **Ready** با یک nit
رفع‌شده. قاعدهٔ encode/decode در `docs/contracts.md §۷.۱` ثبت شد.

**به‌روزرسانی ۲۰۲۶-۱۱-۲۵ (R15-B):** بستهٔ `internal/trayui` به‌عنوان
model/controller خالص (بدون DBus) اضافه شد: `Caller` هم‌شکل `control.Client`،
`Lifecycle` برای Quit/RestartEngine و رویدادهای `restarting`/`ready`، `Notifier` و
`AutostartManager`؛ درخت منو با key پایدار و enum بستهٔ actionها؛ یک pending
confirmation با expiry/replacement/cancel؛ یک job slot با منع درخواست تکراری؛
refresh با fingerprint؛ stopped بر اساس `resume_requires_restart` (true→Restart،
false→resume، nil→محافظه‌کارانه)؛ sanitize/truncate همهٔ labelها. تست‌های fake
caller/control server در-process، جدول کامل actionها، `OK=false`، fixture ۱۷۶
pool/۱۱۷ rule، label مخرب و race سبز؛ `go vet`/`go build`/full tests سبز. یک
اجرای `internal/acceptance` پیش از rerun فوری flake کرد و signature آن در خروجی
truncate‌شده گم شد؛ `internal/trayui` در همان اجرا سبز بود. بازبینی مستقل
B0/B/C پس از R15-C انجام می‌شود.

**به‌روزرسانی ۲۰۲۶-۱۱-۲۵ (R15-C):** `restart-engine` سطح process در supervisor
پیاده شد: کانال دوم per-tray-generation (`ROUTELATCH_TRAY_EVENT_FD`) برای
`restarting`/`ready` و پذیرش فرمان فقط از pipe کنترل همان-uid tray؛ marker
`starting` قبل از teardown، teardown کامل child با pidfd و spawn child جدید،
readiness با generation + `status.ready` + listener mode-aware + همان tray، و
سپس `ready`؛ رد `ready` کهنه با generation/token/pipe تازه. شکست‌ها fail-closed
با exitهای مستند 1/7/8 و بدون `ready` کاذب؛ restart دستی budget خودکار را مصرف
نمی‌کند و quit/lost/error سریال می‌مانند. تست‌های fake child/tray، `-race` و
full tests سبز و بازبینی lifecycle مستقل **Ready**. adapter واقعی deedles
همچنان R15-D2 است.

**به‌روزرسانی ۲۰۲۶-۱۱-۲۵ (R15-D1):** `internal/trayui` یک لایهٔ `Backend`/
`Binder`/`Runner` گرفت: درخت با key پایدار از View ساخته/به‌روز می‌شود (add/
remove/move/update، صفحه در یک Apply دسته‌ای)، View بدون تغییر هیچ فراخوانی
نمی‌سازد، خطای Apply rebuild اجباری و `LastError` دارد، و Runner با cadenceهای
status/list/restart/core، forward رویداد lifecycle، worker غیرمسدودکنندهٔ کلیک و
Stop/Close تمیز کار می‌کند. بدون DBus و بدون کد tray محصول؛ adapter deedles در
D2. تست‌های recording sink، idempotency، ترتیب/حذف، fake clock، race،
`-count=20`/`-race -count=5` و leak سبز و بازبینی مستقل **Ready** (یک panic در
retry اولیه رفع شد). D2 باید کلید batch را با Parent سپس Key حل کند (Add می‌تواند
موقتاً کلید descendant را تکرار کند) و icon/tooltip سطح item را از controller
پر کند.

**به‌روزرسانی ۲۰۲۶-۱۱-۲۶ (R15-D2):** منوی ثابت سه‌آیتمی با controller/runner
واقعی جایگزین شد: `TrayOptions.Socket`/`DataDir` از `store.Paths` و env؛ adapter
deedles با identity مسیر کامل (رفع collision کلید تودرتو)، یک Apply در هر View،
add/move/remove/update، radio/check، separator/submenu، icon/tooltip و
click→Runner.Click؛ lifecycle با نوشتن غیرمسدودکنندهٔ `quit`/`restart-engine` و
خواندن ordered `restarting`/`ready` با رد token کهنه/ready بی‌گیت؛ Notifier
coalesce/dedupe/bounded؛ `ready` فقط پس از registration و `tray.New` failure به
exit 7. اجرای KDE gated با data-dir ایزوله (Plasma/X11، ۰۳:۰۸Z) radio sync،
stop/stopped header، restart فقط تا `ready`، all-direct resume، `kill -9` tray و
re-register و Quit پاک را ثبت کرد؛ هیچ تنظیم میزبان تغییر نکرد. آزمون‌نشده:
Wayland/GNOME، تأیید بصری/اسکرین‌شات، desktop proxy واقعی و actionهای
pool/rules/proposals. **اثر جانبی probe:** اجرای اشتباه `plasmashell/kwin_x11
--version` بدون DISPLAY یک DrKonqi ساخت که بسته شد؛ نشست زنده (PIDهای
۴۳۴۹/۴۳۷۴) دست‌نخورده ماند و هیچ تنظیمی تغییر نکرد.

**به‌روزرسانی ۲۰۲۶-۱۱-۲۶ (R15-E):** audit منوی pool نشان داد ساختار/اکشن‌های
موجود تقریباً کامل است؛ دو انحراف رفع شد: ترتیب `Test selected exit` پیش از
`Test first 20 usable` و label سقف بر پایهٔ `contract.MaxPoolTestExits`. تست‌های
جدید ۱۷۶رکورد/صفحه‌ها/groups ۱۰۰-۴۰-۲۰-۱۶، gating رکوردهای healthy/unknown/
cooling/unsupported/invalid/insecure/stopped، auto-pin، select/remove با
confirmation، job سقف‌دار و `ErrBusy`، legacy unsupported با `state_locked` و
notification sanitize را پوشش می‌دهند. **rerank طبق contracts §۷.۳ sync است، نه
job؛ متن ردیف E اصلاح شد.** KDE برای این ردیف اجرا نشد؛ cap ۲۰ فقط
source-inspected است.

**هدف و نتیجهٔ قابل مشاهده.** tray desktop از یک آیکن با چند آیتم ثابت به سطح
کنترل runtime تبدیل می‌شود: status، mode (شامل stop/restart)، proposals،
approved rules، pool، desktop proxy، diagnostics، backup، autostart toggle و
quit. هر action به همان management op موجود یا پیام lifecycle supervisor نگاشت
می‌شود؛ tray هیچ منطق مسیریابی/pool مستقل ندارد. عملیات نیازمند متن در CLI
می‌مانند و یک آیتم `Terminal menu…` منوی کامل CLI را باز می‌کند.

**تصمیم‌های کاربر (D28، ۲۰۲۶-۱۱-۲۵).**
- عملیات متنی (add rule، add/import pool، diagnose، `config set`، restore بکاپ)
  در CLI/`Terminal menu` می‌مانند؛ دیالوگ `kdialog`/`zenity` یا ورودی متن در tray
  اضافه نمی‌شود.
- زبان منو و notification **English** است؛ i18n خارج از این milestone است.
- کنترل کامل runtime با تأیید دو مرحله‌ای، شامل حذف rule/exit، clear proposals،
  restore پروکسی دسکتاپ، `Stop listening` و `Restart engine` سطح process.
- pool/rules گروه‌بندی و صفحه‌بندی می‌شوند (۲۰–۲۵ آیتم در هر صفحه).
- مدیریت service/systemd، restore بکاپ دلخواه، diagnose متنی و GUI کامل خارج از
  دامنه‌اند. D21/D26 و invariantهای مسیر/pool تغییر نمی‌کنند.

**مرز و واقعیت‌های پایه (بررسی ۲۰۲۶-۱۱-۲۵).**
- tray فعلی در `internal/app/tray.go` فقط Quit، نمایش log path و راهنمای restore
  پروکسی دسکتاپ را دارد؛ هیچ `control.Client` ندارد و فقط pipe
  `quit`/`lost`/`error` به supervisor دارد. پیام `restart-engine` در R15-C
  اضافه می‌شود، نه بخشی از وضعیت فعلی.
- همهٔ عملیات لازم از قبل وجود دارند: `status`، `mode`، `rules.*`، `pool.*`،
  `proposals.*`، `desktop.*`، `backup`، `doctor` و `autostart` محلی. **op جدید
  management لازم نیست.** `pool test` سقف `MaxPoolTestExits=20` دارد و proposals
  فقط در حافظهٔ process هستند (D21).
- `deedles.dev/tray v0.2.0` زیرمنو، separator، radio/checkmark،
  visible/enabled، تغییر label و add/remove آیتم را از source دارد؛ R13-A
  registration/menu click/notification را روی Plasma 6.6.7 اثبات کرد، ولی
  زیرمنوی تودرتو، toggle و به‌روزرسانی زندهٔ منو **آزمون‌نشده**اند.
- مسیرهای canonical instance و socket همان §۷.۵ هستند؛ tray باید socket را از
  `store.Paths.Socket` بگیرد و از همان peer check same-uid استفاده کند.
- `status` در stopped معتبر است؛ ترک stopped در همان process ممنوع است و
  restart سطح process لازم دارد (R-4). R15-B0 یک فیلد additive
  `resume_requires_restart` اضافه می‌کند تا tray (و CLI) بین stopped-by-process
  و process تازه‌ای که در stopped بالا آمده فرق بگذارند؛ بدون آن، وضعیت با
  restart خود tray گم می‌شود. این حالت در UI باید صریح و قابل‌بازیابی باشد.
- toolchain همان Go **1.27.4** pin‌شده در `go.mod` است. در این محیط `go` در
  `PATH` نیست؛ روش R13-A (toolchain module zip تأییدشده زیر `/tmp/opencode`،
  `GOTOOLCHAIN=local`) مرجع است. نصب system-wide Go بدون مجوز لازم نیست.

**قرارداد.** درخت منو، جدول actionها، تأیید دو مرحله‌ای، job/refresh، حالت
stopped، Quit/پروکسی دسکتاپ، Terminal menu و امنیت خروجی در
[`contracts.md`](contracts.md) §۷.۳ آمده‌اند. پیام `restart-engine` و
transitionهای supervisor در [`contracts.md`](contracts.md) §۷.۵ هستند. خلاصهٔ
منو:

```text
Status · Mode · Proposals · Approved rules · Pool · Desktop proxy
Diagnostics · Data · Settings · Advanced · Last action · Quit
```

**دامنهٔ پیشنهادی فایل‌ها.** R15-B0 در `internal/control/ops_status.go` و
تست‌های status؛ R15-B/D1 در بستهٔ جدید `internal/trayui` (model/controller/
runner، مسیر پیشنهادی)؛ R15-C در `internal/app/supervisor.go` و تست‌های موجود
`internal/app/app_test.go`/`integration_test.go` (یا فایل جدید)؛ R15-D2 در
`internal/app/tray.go`/`tray_menu.go` و
`cmd/routelatch/run.go` برای `TrayOptions`؛ R15-E..G2 در فایل‌های domain همان
`internal/trayui` و تست‌هایشان. مسیرهای جدید پیشنهادی‌اند و پس از R15-A/B
تثبیت می‌شوند؛ تغییر هم‌زمان فایل‌های مشترک منو/supervisor بین workerها ممنوع
است.

**کارها.**

| ID | task | نتیجه | وابستگی | check اصلی |
| --- | --- | --- | --- | --- |
| R15-0 | **ثبت و بازبینی پلن — انجام شد (۲۰۲۶-۱۱-۲۵)** | D28/A16/contracts/architecture/plan هم‌خوان و بدون ادعای پیاده‌سازی؛ مسیرها/opها/رفتارها با کد فعلی سنجیده شدند؛ self-review پلن شکاف مادی نداشت و verdict برای R15-A **Ready** است | — | `git diff --check`؛ لینک‌های سند؛ grep برای ادعای کهنه؛ بازبینی با پرسش‌های coverage/grounding/sequence/verification؛ commit مستندات |
| R15-A | **spike قابلیت‌های منوی tray — انجام شد (۲۰۲۶-۱۱-۲۵)** | شاهد واقعی روی Plasma 6.6.7/X11: زیرمنوی تودرتو/separator، radio (بصری)، checkmark (layout/host، بدون اسکرین‌شات)، visible/enabled (D-Bus/host، بدون تصویر پنهان/خاکستری)، add/remove/relabel **در منوی باز**، صفحهٔ ۲۰آیتمی از fixture ۱۷۶ با clamp، و label بلند/Unicode کار می‌کنند؛ `AboutToShow`/`Opened`/`Clicked`/`Closed`، زمان `GetLayout`/`Event` و amplification host ثبت شد. نتیجه برای R15-D1: live apply کافی است و reopen-only لازم نیست؛ صفحه‌گردانی ۲۰ردیفی ۵۱ `GetLayout` می‌سازد و باید دسته‌بندی شود. شکاف‌ها: اسکرین‌شات checkmark و سطح سوم زیرمنو، و آزمون Wayland/GNOME. شاهد: `spike/tray/MENU-FINDINGS.md` و `spike/tray/evidence/menu-*-2026-11-25.*`؛ build چهار معماری static؛ هیچ کد محصول | R15-0 | KDE gated انجام شد (یک آیتم موقت، حذف‌شده)؛ offline با fake watcher؛ `GetLayout`/`Event`/`SetProps` در `evidence/`؛ snapshot در `research.md`؛ verdict برای R15-B0 **Ready** |
| R15-B0 | **`status.resume_requires_restart` — انجام شد (۲۰۲۶-۱۱-۲۵)** | فیلد `ResumeRequiresRestart *bool` (tag `resume_requires_restart`) در `StatusData`؛ `status()` همیشه مقدار صریح true/false می‌فرستد و منبع واحد `s.stopped && s.stoppedByThisProcess` است؛ process تازه در stopped → false؛ targeted/all-proxy → false؛ payload قدیمی بدون فیلد در مشتری `nil`/unknown می‌ماند؛ `schema_version` ۱ بدون تغییر؛ هشدار stopped-by-process و process تازه متفاوت؛ قاعدهٔ encode/decode در `docs/contracts.md §۷.۱`؛ تست‌های focused و full tests سبز (acceptance ۱۳۰٫۱s)؛ بازبینی مستقل **Ready** با یک nit رفع‌شده | R15-A | تست control برای stopped-by-process، process تازه در stopped و targeted؛ JSON additive؛ هشدار درست؛ decode JSON بدون فیلد؛ سازگاری مشتری قدیمی؛ `go test ./internal/control/...` |
| R15-B | **controller/model در `internal/trayui` — انجام شد (۲۰۲۶-۱۱-۲۵)** | بستهٔ جدید `internal/trayui` (فقط model/controller، بدون DBus): snapshot تایپ‌شده از status/rules/pool/proposals/desktop/autostart/env/config/paths؛ `Caller` هم‌شکل `control.Client` (envelope با `OK=false` شکست است)، `Lifecycle` (Quit/RestartEngine + رویدادهای bounded `restarting`/`ready`) و `Notifier`/`AutostartManager` برای adapter آینده؛ `Controller.View/Refresh*/Trigger/Confirm/HandleEvent/Tick/SetPage`؛ درخت با key پایدار و enum بستهٔ ۳۲ action با نگاشت صریح به request/target؛ یک pending confirmation با expiry ۶۰s/replacement/cancel و یک job slot با منع تکرار؛ fingerprint روی snapshot پاک‌شده + pending/job/page/restart (بدون rebuild در دادهٔ ثابت)؛ sanitize همهٔ labelهای داینامیک با `control.SanitizeTerminal` و truncate؛ stopped با tri-state `resume_requires_restart` (true → Restart engine، false → resume، nil → محافظه‌کارانه)؛ بدون dependency جدید و بدون منطق routing/pool | R15-A/B0 | fake caller و control server در-process؛ جدول کامل actionها؛ sequence رویداد restart؛ pending expiry/replacement/cancel؛ `OK=false`؛ fixture ۱۷۶ pool و ۱۱۷ rule؛ page clamp؛ label مخرب ESC/CR/bidi؛ no duplicate request؛ `go test -race`، `go vet`، full tests سبز (یک اجرای acceptance flaked با signature گم‌شده، rerun فوری سبز؛ `internal/trayui` در همان اجرا سبز بود) |
| R15-C | **restart engine در supervisor — انجام شد (۲۰۲۶-۱۱-۲۵)** | کانال دوم per-tray-generation (fd 5، `ROUTELATCH_TRAY_EVENT_FD`) با write غیرمسدودکنندهٔ `restarting <token>`/`ready <token>`؛ `restart-engine` فقط از pipe کنترل همان-uid tray؛ marker `starting` پیش از teardown، سپس `restarting`؛ teardown کامل child قدیمی با pidfd، spawn `run --headless` جدید، readiness با generation نسل جدید + `status.ready` + listener mode-aware + همان tray ثبت‌شده، سپس `ready` marker/event؛ رد `ready` کهنه با generation/token/pipe تازه؛ شکست marker/teardown/spawn → fail-closed exit 1، timeout/child exit/tray گم‌شده → exit 8/1/7 مستند و هرگز `ready`؛ restart دستی از budget خودکار مصرف نمی‌کند؛ restart دستی حین startup/restart خودکار نادیده گرفته می‌شود، پیام‌های صف‌شده حین یک restart دستی در drain بعدی حذف می‌شوند (tray سریال می‌کند؛ best-effort)، و quit/lost/error سریال می‌مانند؛ هیچ restart درون‌process | R15-B | fake child/tray: success، quit حین restart، شکست child، tray گم‌شده، marker transition، stopped؛ نبود موفقیت کاذب با `ready` کهنه؛ timeout/رویداد گم‌شده → unconfirmed؛ no in-process restart؛ `go test ./internal/app/...` ۱۹s، `-race` ۱۰۸s و full tests (acceptance ۱۱۸٫۵s) سبز؛ بازبینی lifecycle مستقل **Ready** (۱۵ تست و F2×۱۰ rerun) |
| R15-D1 | **binder/runner با backend ساختگی — انجام شد (۲۰۲۶-۱۱-۲۵)** | `Backend{Build,Apply,Close}` روی `MenuItem` با key پایدار/نوع/visible/enabled/radio/checkbox/icon/tooltip و `Tray`؛ `Binder` اعتبارسنجی کلید/عمق، `Build` اولیه و یک `Apply` deterministic در هر View تغییرکرده (removal/add/move/update؛ صفحهٔ ۴۰عملیاتی در یک فراخوان)، View ثابت بدون فراخوان؛ خطای Apply کلیک‌ها را پاک و rebuild اجباری می‌کند و `LastError`/log دارد؛ `Runner` با Loading اولیه و `RefreshAll` async، cadenceهای status/list/restart/core، forward رویداد lifecycle، worker غیرمسدودکنندهٔ کلیک با همان مسیر Trigger/Confirm، رد کلیک disabled/unknown/stopped و Stop/Close تمیز | R15-B/C | recording sink؛ apply دوباره idempotent؛ تغییر ترتیب/حذف/relabel؛ event/click؛ handler غیرمسدودکننده؛ fake clock؛ `-count=20` و `-race -count=5` سبز؛ نبود fd/goroutine leak؛ full tests (acceptance ۱۱۶٫۹s) سبز؛ بازبینی مستقل **Ready** (یک panic در retry اولیهٔ Start پیدا و رفع شد) |
| R15-D2 | **اتصال واقعی tray و actionهای هسته — انجام شد (۲۰۲۶-۱۱-۲۶)** | `TrayOptions.Socket`/`DataDir` از `store.Paths` و env (`ROUTELATCH_TRAY_SOCKET`/`ROUTELATCH_DATA_DIR`)؛ `internal/app/tray_menu.go` adapter واقعی deedles برای Build/Apply/Close با identity مسیر کامل (رفع collision کلید تودرتو)، add/move/remove/update، radio/check clearing، separator/submenu، icon/tooltip و click→Runner.Click؛ `tray_lifecycle.go` نوشتن غیرمسدودکنندهٔ `quit`/`restart-engine`، خواندن ordered `restarting`/`ready` با رد token کهنه/ready بی‌گیت، Notifier coalesce/dedupe/bounded؛ wiring `control.Client{Path: Socket}`، autostart/paths و `ready` فقط پس از registration؛ `tray.New` failure→`tray_unavailable`/exit 7 | R15-D1 | واحد/race/full tests سبز (acceptance ۱۲۴٫۰s)؛ build استاتیک چهار معماری؛ KDE واقعی Plasma/X11 با data-dir ایزوله (۰۳:۰۸Z): radio sync، stop/stopped header، restart فقط تا event `ready`، all-direct resume، `kill -9` tray و re-register، Quit پاک؛ بدون تغییر تنظیم میزبان. **آزمون‌نشده:** Wayland/GNOME، تأیید بصری/اسکرین‌شات، desktop proxy واقعی، actionهای pool/rules/proposals. **اثر جانبی ثبت‌شده:** probe اشتباه `plasmashell/kwin_x11 --version` بدون DISPLAY یک DrKonqi ساخت که بسته شد؛ نشست زنده (PIDهای ۴۳۴۹/۴۳۷۴) دست‌نخورده ماند |
| R15-E | **pool — انجام شد (۲۰۲۶-۱۱-۲۶)** | audit نشان داد pool tree/actionها تقریباً کامل بودند؛ دو انحراف قراردادی رفع شد: ترتیب `Test selected exit` قبل از `Test first 20 usable` (مطابق §۷.۳) و label بر پایهٔ `contract.MaxPoolTestExits` به‌جای ۲۰ hardcoded. بقیهٔ summary/auto-pin/groups/pagination/remove confirmation/refresh-test jobs/disabledها از B/D2 موجود بود. **rerank مطابق contracts §۷.۳ sync است، نه job؛ ادعای job در این ردیف اصلاح شد** و کد قرارداد را دنبال می‌کند | R15-D2 | fixture ۱۷۶ رکورد و گروه‌ها ۱۰۰/۴۰/۲۰/۱۶؛ صفحه ۲۰ردیفی و clamp؛ select/test gating برای healthy/unknown/cooling/unsupported/invalid/insecure/stopped؛ auto/pin radio؛ select+remove confirmation یک‌بار؛ job سقف‌دار بدون ID و `ErrBusy` تکراری؛ selected-exit target؛ legacy unsupported از `pool.Build` واقعی و خطای `state_locked`؛ notification sanitize؛ race/`-count=20`؛ full tests (acceptance ۱۱۸٫۰s) سبز. **آزمون‌نشده:** cap ۲۰ به‌صورت end-to-end اجرا نشد (source-inspected در `ops_pool.go`)؛ KDE برای این ردیف skip شد |
| R15-F | **rules + proposals — انجام شد (۲۰۲۶-۱۱-۲۶)** | audit نشان داد implementation کامل بود؛ یک ناسازگاری availability رفع شد: `allowedLocked` اکنون Recheck را برای proposalهای unroute رد می‌کند تا منو و کلیک کهنه یک قاعده داشته باشند و override جدا در `menu.go` حذف شد. rules با pagination/remove confirmation/selected ID؛ proposals pending/decided/expired؛ accept route و unroute؛ reject/recheck/clear دقیقاً یک‌بار؛ unroute recheck blocked؛ malicious ID/key/confirm label؛ stale confirm همان proposal ID و expired confirm بدون request؛ concurrent confirm/refresh. بدون management op/persistence جدید | R15-D2 (پس از E) | fixture ۱۱۷ rule در ۶ صفحه بدون تکرار؛ accept route/unroute؛ reject/recheck/clear؛ label مخرب؛ D21 scan (بدون file-write در trayui؛ canary destination/URL/provider در notification/result/last-action نیست)؛ race و full tests (acceptance ۱۱۷٫۷s) سبز. **آزمون‌نشده:** KDE برای این ردیف؛ `proposals clear` retention ۳۰روزه همان semantics موجود است |
| R15-G1 | **desktop/diagnostics/data/settings — انجام شد (۲۰۲۶-۱۱-۲۶)** | audit چهار gap کاربرمحور را رفع کرد: `Desktop Refresh status` در محیط unsupported غیرفعال شد (failed read همچنان recovery دارد)؛ نتیجهٔ `desktop.restore` برای no-op/user-modified صریح شد («nothing to restore» / «N user-modified settings left unchanged»)؛ `notice` (changed/unverifiable ownership) به‌صورت یک ردیف disabled نمایش داده می‌شود؛ `conflict=service` به‌جای کلمهٔ خام، تعارض unit مدیریت‌شده را توضیح می‌دهد. desktop set/restore با confirmation و single request؛ stopped/listenerless/unsupported/read-failure؛ doctor/warnings/env/log path؛ backup؛ custom data-dir/config path؛ autostart conflict/foreign/error/no-manager؛ D21 outcome scan و static no-fs-import | R15-D2/E/F | fake desktop/autostart؛ set/restore confirmation؛ custom data-dir؛ D21؛ race؛ trayui/app/full tests سبز (acceptance ۱۱۷٫۹s). **آزمون‌نشده:** desktop/autostart واقعی (KDE/live)؛ ردیف notice افزودنی است و در درخت قرارداد صریح نیامده ولی از الگوی reason-row پیروی می‌کند |
| R15-G2 | **Terminal menu و Quit — انجام شد (۲۰۲۶-۱۱-۲۶)** | Terminal menu که قبلاً فقط «requested» بود، با boundary `trayui.Terminal` و `internal/app/tray_terminal.go` واقعی شد: `konsole -e`/`gnome-terminal --`/`xterm -e` با argv صریح و `<exe> [--data-dir <abs>] menu`، `Setsid`، بدون shell/$TERMINAL/user text؛ terminal گم‌شده `ErrNoTerminal` با دستور دستی sanitized و بدون request؛ fdهای سوپروایزر close-on-exec. Quit سطح کامل audit شد: applied/unknown warning و restore/keep صریح، شکست restore بدون quit، keep بدون restore، job confirmation و Last action نهایی | R15-G1 | fake terminal در PATH (argv دقیق، failure، missing، hostile `$TERMINAL`، descriptor sealing)؛ quit not-applied/applied/unknown/restore-success/restore-failure/keep/job؛ action-surface invariant در loading/ready/applied/stopped/unavailable/pending-job؛ D21 canary؛ race/full tests (acceptance ۱۱۷٫۱s) و static چهار معماری سبز. **ریسک:** بدون KDE/live؛ دستور دستی space-joined برای مسیرهای دارای فاصله مبهم است؛ restore-and-quit هنگام job دیگر disabled می‌ماند. **بازبینی سطح کامل پس از G2:** Ready with findings؛ پنج یافته (پیام restore در Quit، دستور دستی بلند، identity pending، Loading، متن‌ها) رفع و با تأیید متمرکز بسته شدند |
| R15-H | **acceptance/docs/review/release gate — انجام شد (۲۰۲۶-۱۱-۲۶)** | acceptance روی `22cb97d` + H changes: gofmt/vet/build، `go test ./...` (۱۳۴s)، `-race` trayui/app/control (۱۰۹s)، build استاتیک چهار معماری، `check-artifact.sh` و `check-package.sh` در `DIST` ایزوله با `REQUIRE_SINGBOX=1` سبز. A16 روی data-dir ایزوله/Plasma/X11: status/radio، stop+confirm، restart تا ready، resume، unavailable/recovery، rules/pool pagination و rule-remove، ESC/۸۰rune، canary proposal+Reject، desktop read-only، doctor/log/env، backup ایزوله، fake-konsole Terminal argv، plain Quit و ۱۲ notification بدون مقصد/URL/credential. **نقص واقعی:** `-race` اول در ۳/۲۰۰ اجرا یک ترتیب label/click-target را نشان داد؛ `internal/trayui/backend.go` اکنون targetها را پیش از Apply منتشر و در failure پاک می‌کند، با regression deterministic؛ rerunها سبز. D21 canary فقط در proposal زنده دیده شد و در data/config/state/backup/XDG/log/notification نبود؛ diff scan بدون secret/private path. README/deploy/docs/contracts/architecture به‌روز؛ شاهد: `spike/tray/evidence/r15h-acceptance-2026-11-26.md` و `r15h-2026-11-26/`. بازبینی نهایی مستقل **Ready with findings** (شاهد گم‌شده copy شد، claimهای غیرآرشیوی شفاف شدند) | R15-B0–G2 | checks بالا؛ KDE gated؛ D21 scan با `dbus-monitor`؛ no-secret scan؛ push با runهای سبز CI `45795386223` (fe155c4) و `45795547961` (71b3c05)؛ انتشار `v1.1.0` با مجوز ایستادهٔ ۲۰۲۶-۱۱-۲۶. **آزمون‌نشده:** Wayland/GNOME، اسکرین‌شات/بصری، desktop set/restore واقعی، autostart، proposal accept/recheck/clear، pool remove/test، non-amd64 runtime و service install |

**ترتیب و دروازه‌ها.** R15-0 → R15-A (دروازهٔ فنی) → R15-B0 → R15-B → R15-C →
R15-D1 → R15-D2 → R15-E → R15-F → R15-G1 → R15-G2 → R15-H. R15-A باید
مشخص کند تغییرات زنده در همان منوی باز دیده می‌شوند یا فقط در `GetLayout`
بعدی؛ اگر فقط دومی، refresh روی بازشدن دوباره fallback پذیرفته است و actionها
عوض نمی‌شوند. اگر R15-A یک قابلیت لازم را رد کرد،
قبل از B با کاربر تصمیم بگیر؛ fallback بی‌صدا یا حذف یک action بدون ثبت تصمیم
ممنوع است. E و F فقط وقتی می‌توانند موازی شوند که D2 رابط‌های مشترک را freeze
کرده و مالکیت فایل‌ها جدا باشد؛ پیش‌فرض، ترتیب است. بازبینی مستقل پس از C
(controller + restart) و پس از G2 (کل سطح action) و verdict نهایی در H انجام
می‌شود؛ هر دور بعدی فقط یافته‌های باز و اثر رفع‌ها را بررسی می‌کند.

**اجرا با orchestrator.** هر session با `AGENTS.md`، همین بخش، D28/A16 و
`contracts.md §۷.۳/§۷.۵` شروع می‌شود. orchestrator وضعیت `git status`/HEAD و
آخرین checkها را با رکورد مقایسه می‌کند، یک task رفع‌مانع را انتخاب می‌کند و به
worker یک brief خودکفا می‌دهد: هدف، مرز، تصمیم‌های بسته، فایل‌های مالکیت،
checkهای مورد انتظار و ممنوعیت تغییرات نامرتبط. worker پس از هر تغییر تست‌های
مربوط را اجرا می‌کند؛ orchestrator نتیجه را می‌سنجد، ردیف R15 را با شاهد واقعی
به‌روز می‌کند و تغییر منسجم را commit می‌کند. push پس از batch تأییدشده طبق
`AGENTS.md` است؛ tag/release یک batch واجد شرایط طبق همان سیاست (مجوز ایستادهٔ
۲۰۲۶-۱۱-۲۶) مجاز است. اگر worker به
تصمیم کاربر رسید، سؤال را به کاربر می‌رساند و gate را ضعیف نمی‌کند. هیچ worker
نباید هم‌زمان با دیگری روی فایل‌های مشترک منو/supervisor کار کند.

**پذیرش (A16).** R15-B0..G2 باید هر ردیف A16 را پوشش دهند: نگاشت کامل actionها،
تأیید دو مرحله‌ای، صفحه‌بندی، حالت‌های unavailable/stopped، sanitize، عدم
مقصد/URL در log/notification، Quit/restore و restart. A10/A15 و رفتار v1
باید رگرسیون‌نشده بمانند؛ D21 با یک مقصد یکتا در proposal و سپس grep
log/state/config/backup بررسی می‌شود. ادعای «menu روی KDE کار می‌کند» فقط با
شاهد D-Bus/بصری ثبت‌شده معتبر است. KDE gated برای منو و actionهای
غیرمیزبانی است؛ `desktop proxy set/restore` واقعی فقط با مجوز جدا و در محیط
ایزوله اجرا می‌شود و در آزمون پیش‌فرض نیست.

**ریسک‌ها و مجوز.** (۱) رفتار host در update زنده/زیرمنو ممکن است با source
فرق کند؛ R15-A آن را می‌بندد. (۲) restart supervisor مسیر lifecycle حساس است؛
تست/بازبینی مستقل لازم دارد. (۳) حالت stopped و بازگشت آن نیازمند state machine
صریح در tray است. (۴) notification rate limit در Plasma دیده شده؛ notification
ها باید coalesce شوند. (۵) ترمینال ممکن است نصب نباشد؛ fallback دستور دستی
لازم است. (۶) autostart محلی wiring از `cmd` می‌خواهد؛ نباید به سرویس/state
دوم تبدیل شود. (۷) GNOME/سایر DEها همچنان آزمون‌نشده‌اند. (۸) `pool test`
فقط ۲۰ exit و بدون progress است؛ UI نباید بیشتر ادعا کند. (۹) jobها با
restart/unavailable قطع می‌شوند و پیام «interrupted» لازم دارند. (۱۰) i18n
خارج از دامنه است. **مجاز:** کد/تست/build محلی و commit مستندات. **نیازمند
مجوز جدا:** اجرای tray روی KDE واقعی با data-dir ایزوله (می‌تواند یک آیتم tray
و listener موقت بسازد؛ هیچ تنظیم دسکتاپ تغییر نکند)، تغییر واقعی پروکسی دسکتاپ،
و نصب/فعال‌سازی autostart/service. tag/release یک batch واجد شرایط طبق سیاست
`AGENTS.md` مجاز است.

**قدم بعد.** R15-0 تا R15-H بسته شدند؛ شاهد منو در
`spike/tray/MENU-FINDINGS.md`، `status.resume_requires_restart`، controller/model
`internal/trayui`، binder/runner، اتصال واقعی deedles و رکورد پذیرش
`spike/tray/evidence/r15h-acceptance-2026-11-26.md` ثبت شده‌اند. بازبینی‌های
مستقل B0/B/C، D2 و سطح کامل action انجام و یافته‌هایشان رفع شدند؛ بازبینی نهایی
H نیز **Ready with findings** بود و شاهد گم‌شده/claimهای غیرآرشیوی اصلاح شدند.
انتشار `v1.1.0` نیز انجام شد (تگ `7abe9704` روی `532c169`، `Release`
`45800238447`، Release id `459257636`)؛ هیچ کار باز R15 نمانده است. موارد
آزمون‌نشده (Wayland/GNOME، اسکرین‌شات، desktop set/restore، autostart، proposal
accept/recheck/clear، pool remove/test، runtime غیر-amd64 و service install)
فقط با مجوز/محیط جدا انجام می‌شوند و کار باز R15 نیستند.

### R14 — ساخت و انتشار ریلیز در GitHub Actions (تعریف ۲۰۲۶-۱۱-۲۴؛ انجام و بسته ۲۰۲۶-۱۱-۲۵)

**وضعیت واحد (a) — انجام شد (۲۰۲۶-۱۱-۲۴):** `.github/workflows/release.yml` و
`scripts/release-assets.sh` نوشته و روی branch `release-dryrun` اجرا شدند.
dry-run سبز `45397329232` (commit `57e162c`) و dry-run نهایی دور اول `45400769014`
(commit `2cdf05f`) هر ۱۷ دارایی را ساختند/تأیید کردند، publish skip شد و
releases API هیچ Release عمومی تازه‌ای نداشت (listing بدون احراز هویت draftها را
نشان نمی‌دهد). شکست کنترل‌شده `45399453678`
(commit `37208c8`؛ خرابی موقت یک tarball پیش از `check-package.sh`) job را fail
کرد و jobهای RPM/assemble/publish skip شدند؛ سپس injection حذف شد. پس از دور
اصلاح، dry-run سبز `45422318159` روی commit `952276e` (اصلاح guard/cleanup) هر
چهار job ساخت/تأیید را گذراند (SHA256SUMS یکسان در دو اجرا، `sha256sum -c`، چهار
RPM بازرسی‌شده و مجموعهٔ دقیق ۱۷ دارایی) و publish skip شد. سپس commit `2c8fa1c`
mode فایل `scripts/release-verify-upload.sh` را از `100644` به `100755` رساند
(blob بدون تغییر، runهای سبز Release `45427473470` و CI `45427473102`) و commit
`02e3226` پیام شکست همان script را برای نام‌بردن دارایی گم‌شده/اضافی اصلاح کرد؛
dry-run سبز نهایی `45463989732` روی `02e3226` دوباره هر چهار job ساخت/تأیید را
گذراند (CI `45463989826` سبز)، publish skip شد و releases/tags API بدون تغییر
ماند (۵ Release عمومی و ۵ تگ). blob نهایی workflow در همهٔ این runها
`e723d83a83a9e86bf3c5c0d3e1ae7abf5fffd2c3` است و commitهای بعدی فقط مستندات را
تغییر می‌دهند، نه workflow یا script. **بازبینی و تحویل (۲۰۲۶-۱۱-۲۴):** دور اول
**Not ready** بود چون پاک‌سازی `if: failure()` می‌توانست در اجرای دوباره، Release
عمومی موجود را حذف کند؛ رفع در `952276e` و دور دوم **Ready with findings** شد و
دو یافتهٔ جزئی (استناد کهنهٔ مستندات و نام‌بردن دارایی در پیام شکست) در
`02e3226`/`e85b9b7` رفع و با تأیید کوتاه بسته شدند. branch `release-dryrun` با
fast-forward به `main` (`0183e3f`) رسید و به هر دو remote push شد؛ تگ‌ها و
ریلیزها دست‌نخورده‌اند. تنها قدم باقی‌مانده (c) اولین تگ/انتشار واقعی با مجوز
صریح است. `RELEASE_BUILD=1` فقط
برای بسته‌های CI متن `PACKAGE-NOTES` و هدر spec را عوض می‌کند و
`check-package.sh` در همین حالت ادعای «local build» را رد می‌کند؛ رفتار محلی
بدون این متغیر دست‌نخورده و آزموده شد. **دور اصلاح بازبینی:** guard انتشار
اصلاح شد؛ `scripts/release-guard.sh` وجود Release عمومی یا draft از پیش موجود را
با پیام قابل‌اقدام رد می‌کند و چیزی را حذف نمی‌کند، و پاکسازی فقط draftی را حذف
می‌کند که output مرحلهٔ ساختِ همان run (`created=true`) آن را مجاز کرده و
بلافاصله پیش از حذف دوباره `.draft == true` خوانده شده باشد. بررسی آپلود با
`scripts/release-verify-upload.sh` به‌جای یک خواندن، digest/size را با polling
محدود می‌سنجد و publish تا کامل‌شدن آن اجرا نمی‌شود. RPMهای باینری با rpm
بدون‌pin رانر ساخته می‌شوند؛ متن `PACKAGE-NOTES`/README همین را می‌گوید و
`RELEASE_BUILD=1` فقط همراه `GITHUB_ACTIONS=true` پذیرفته می‌شود. **در آن زمان
آزمون‌نشده بود:** job انتشار (draft، ۱۷ آپلود، مقایسهٔ digest و publish) هرگز
اجرا نشده بود و اولین run آن همان اولین تگ واقعی با مجوز جداست؛ gateهای
tag/on-main/CI-green در dry-run اجرا نمی‌شوند.
guard/cleanup و polling digest فقط با `curl`/`gh` stubشده در یک dir موقت آزموده
شدند (۳۱ check، شامل Release عمومی موجود، draft از پیش موجود، خطای API، حذف
فقط draft همان run و digest ناقص)؛ رفتار واقعی API گیت‌هاب آزموده نشده است.
تلاش سوم (پایین) تا ساخت draft و آپلود ۱۷ دارایی رسید، ولی verify/publish هنوز
pass نشده‌اند. جزئیات و شواهد در `docs/release-readiness.md` («CI release pipeline»).

**تلاش سوم و رفع دو نقص (۲۰۲۶-۱۱-۲۵):** commit `4121481` push شد و CI آن سبز
بود؛ تگ `v1.0.1` (شیء `cce34dcb`) روی آن بازسازی شد. اجرای `Release`
`45510002182` تا job انتشار `138857993885` رسید: draft ساخته و ۱۷ دارایی آپلود
شد، ولی step تأیید آپلود شکست خورد؛ چیزی منتشر نشد. علت اول: GitHub برای draft
در `GET /releases/tags/{tag}` مقدار 404 می‌دهد، بنابراین بررسی از اولین درخواست
شکست خورد (نه به‌خاطر دارایی گم‌شده) و cleanup هم با همان lookup 404 دید و draft
(id `457460945`) را باقی گذاشت. علت دوم: step یادداشت‌ها
`git tag -l --format='%(contents)'` را روی ref lightweight همان checkout اجرا
می‌کرد و پیام commit را می‌خواند؛ body همان draft پیام commit بود، نه پیام تگ
annotated. **رفع:** create، id عددی draft را از listing احرازشده (شامل draftها،
صفحه‌بندی‌شده) با retry کوتاهِ محدود می‌گیرد و فقط بعد از آن `created=true`
می‌دهد؛ verify با
`GET /releases/{id}` و الزام tag/draft/۱۷ دارایی/digest و retry محدود 404/5xx
کار می‌کند؛ `check` همهٔ صفحه‌های listing را می‌خواند و هر Release با همان tag
(draft یا عمومی) را با id و وضعیت رد می‌کند و خطای API را fail-closed می‌بندد؛
`delete-draft` فقط با id و پس از تأیید دوبارهٔ draft و tag حذف می‌کند؛ step
یادداشت‌ها تگ واقعی را force-fetch می‌کند، نوع `tag` را الزام می‌کند و
`%(contents)` را از همان شیء می‌خواند. آپلود (`gh release upload`) و publish
(`gh release edit`) همچنان با نام tag کار می‌کنند و draft را خود gh پیدا می‌کند؛
فقط create/verify/cleanup با id عددی کار می‌کنند. trigger/permission/gate تغییر نکرد.
**آزمون:** ۳۱/۳۱ در `/tmp/opencode/rl-draft-id` با `curl`/`gh` stub و یک remote
محلی (شامل بازتولید شکست قدیمی و نقص پیام commit)؛ `sh -n`/`bash -n` و YAML pass.
**باقی (اکنون بسته):** draft `457460945` با مجوز صریح کاربر حذف شد و listing
احرازشده نبود هیچ Release یا draftی برای `v1.0.1` را تأیید کرد؛ سپس تگ روی commit
جدید CI-سبز بازسازی شد.

**نتیجهٔ نهایی (۲۰۲۶-۱۱-۲۵):** تگ annotated `v1.0.1` (شیء `4462cc86`) روی
`d14da10` ساخته و به هر دو remote push شد؛ `CI` `45517940277` روی همان commit سبز
بود. `Release` `45521099138` (job انتشار `138895476159`) منتشر شد و Release عمومی
`v1.0.1` (id `457516096`) با ۱۷ دارایی، نام‌های نسخه‌درست، digests منطبق با
`SHA256SUMS` و body برابر پیام تگ تأیید شد. R14 بسته است.

تصمیم D27: از این پس ریلیز در CI ساخته و منتشر می‌شود و هیچ دارایی‌ای از ماشین
محلی آپلود نمی‌شود. trigger انتخاب‌شده **push تگ annotated `v*`** است و همان
push مجوز انتشار خودکار است؛ توقف برای تأیید دستی در جریان عادی وجود ندارد
(draft فقط برای اتمیک‌بودن آپلود است و همان workflow آن را عمومی می‌کند).

**نتیجهٔ قابل مشاهده:** push یک تگ نسخه، یک GitHub Release عمومی با همان ۱۷
دارایی (چهار tarball، چهار `.deb`، چهار spec، چهار RPM باینری و `SHA256SUMS`)
می‌سازد؛ هیچ مرحله‌ای روی ماشین کاربر اجرا نمی‌شود. ریلیزهای `v0.1.0` تا
`v1.0.0` و دارایی‌هایشان دست‌نخورده می‌مانند و `v1.0.1` اولین ریلیزی است که از
همین مسیر منتشر شده است. Forgejo فقط ref/tag را mirror
می‌کند و دارایی Release ندارد.

**دامنه:**
- workflow جدید `.github/workflows/release.yml` (نام پیشنهادی) با
  `on: push: tags: ['v*']` و `permissions: contents: write` فقط برای job
  انتشار؛ checkout با تاریخچهٔ کامل برای خواندن پیام تگ annotated.
- بازاستفاده از `scripts/build.sh`، `scripts/package.sh`،
  `scripts/check-artifact.sh` و `scripts/check-package.sh` با `VERSION` برابر
  نام تگ، `COMMIT` برابر commit تگ، `CGO_ENABLED=0` و
  `SOURCE_DATE_EPOCH=1700000000`.
- دانلود آرشیوهای رسمی pin‌شدهٔ sing-box v1.14.4 برای amd64/arm64، تأیید
  SHA-256 با مقادیر داخل `scripts/package.sh` و استخراج در `dist/singbox`؛
  `REQUIRE_SINGBOX=1` تا release بدون sidecar منتشر نشود.
- ساخت چهار RPM باینری در CI (همان روش job `rpm-packages` یا container
  `ubuntu:26.04`)، بازرسی متادیتا/payload و افزودن آن‌ها به دارایی‌ها؛
  `SHA256SUMS` نهایی باید هر ۱۶ فایل را پوشش دهد.
- publish اتمیک: ابتدا draft Release ساخته شود، هر ۱۷ دارایی آپلود و
  نام/اندازه/digest آن‌ها از API تأیید شود، سپس همان workflow draft را عمومی
  کند؛ اگر هر مرحله شکست بخورد Release عمومی ناقص باقی نمی‌ماند.
- یادداشت انتشار از پیام تگ annotated خوانده شود؛ release note خالی مجاز
  نیست. مستندات `README.md` و `packaging/README.md` دربارهٔ مسیر release،
  sidecar و PACKAGE-NOTES در همان تغییر به‌روز شوند؛ PACKAGE-NOTES بسته‌های
  CI نباید بگوید «local build, not published».

**خارج از دامنه:** بازسازی/جابه‌جایی ریلیزهای گذشته؛ انتشار دارایی روی Forgejo؛
امضای GPG، مخزن بسته، Windows و آزمون runtime معماری‌های غیر amd64؛ تغییر
رفتار محصول.

**پیش‌نیازها و ریسک‌ها:**
- تگ باید annotated و روی `main` باشد و full CI روی همان commit سبز باشد
  (اجرای دوبارهٔ همان jobها در release workflow یا خواندن نتیجهٔ CI همان SHA)؛
  هیچ releaseای از commit ناموفق ساخته نمی‌شود.
- «انتشار خودکار با push تگ» یعنی یک تگ اشتباه می‌تواند عمومی شود؛ محافظ‌ها
  الگوی `v*`، الزام annotated tag، بررسی `main`، اجرای کامل gate و publish
  فقط پس از تأیید دارایی‌هاست. اصلاح/حذف Release اشتباه یک عملیات دستی جدا و
  نیازمند مجوز است.
- `GITHUB_TOKEN` تنها credential است؛ هیچ secret، مسیر شخصی یا ابزار محلی
  وارد workflow نمی‌شود.
- برای آزمون بدون انتشار، یک حالت `workflow_dispatch` با `publish=false`
  (یا اجرای همان jobها روی branch) پیش‌بینی شود که کل ساخت و بازرسی را اجرا
  می‌کند و Release نمی‌سازد؛ اولین انتشار واقعی همچنان مجوز صریح کاربر
  می‌خواهد.

**معیار پذیرش:**
- YAML و shellها parse شوند؛ dry-run کامل روی branch، هر ۱۷ دارایی را بسازد،
  `check-artifact.sh` و `check-package.sh` pass شوند، دو اجرای packaging همان
  `SHA256SUMS` را بدهد و `sha256sum -c` سبز باشد، بدون ایجاد Release.
- یک شکست کنترل‌شده در dry-run (مثلاً checker) ثابت کند Release عمومی ساخته
  نمی‌شود.
- اولین push تگ واقعی (با مجوز جدا) Release عمومی با ۱۷ دارایی بسازد؛ API
  نام/اندازه/digest هر دارایی را برابر artifact CI نشان دهد، تگ به commit
  موردنظر اشاره کند و هیچ دارایی اضافه/ناقص نباشد.
- شاهد ثبت‌شده در `docs/release-readiness.md` و همین بخش: run id، commit،
  checksumها و نتیجهٔ verify.

### R13 — مدل اجرای app/tray و سرویس اختیاری (شروع ۲۰۲۶-۱۱-۲۳)

**وضعیت:** تصمیم D26 ثبت شد و مستندات طراحی (`requirements.md`، `architecture.md`،
`contracts.md`، `research.md`) به‌روز شدند. در آن مرحله هیچ کد، بسته، unit،
autostart یا تنظیم میزبان تغییر نکرده بود؛ R13-A/B بعداً کد افزودند. HEAD پیش از
commit مستندات: `de70f20`؛ commit D26:
`5dbc070`؛ commit اصلاحات دور اول: `973d5a8`؛ دور دوم `76dd6d6`؛ دور سوم
`a7fa3b6`؛ دور چهارم `b623567`؛ دور پنجم `f9bcd45`؛ دور ششم `5a28874`؛ دور هفتم
`53ebf68`؛ دور هشتم `eddcc4e`؛ دور نهم `58c4430`؛ دور دهم `2d5db69`؛ دور یازدهم
`7407a6d`؛ دور دوازدهم `ee28f23`. بازبینی‌های دور اول تا یازدهم **Not ready**
بودند
(current-state unit، singleton/app marker، tray lifetime، coexistence، migration،
ownership، startup-in-progress، readiness/rollback، managed-path symlink،
adoption، دادهٔ run، lock legacy migration، attribution marker، readiness
listener، rollback branch، exit `1/9`، teardown timeout، legacy lock set، marker
false-0 و in-place write، XDG base pinning، rollback stop-flag، exit-1 restart
race، repair re-flock، newline framing، inactive rollback branch، grandchild
teardown، legacy symlink dedupe، unit path shell/manager، PID/PGID reuse، ready
زودهنگام در startup-phase repair، pidfd open/identity order، child-death wait،
ready کهنه پس از engine death/Quit/tray-loss، deleted-marker stopping،
stopping-write failure، startup pidfd failure/ESRCH، انتشار R-21 به autostart/
docs، استثنای SIGKILL خام در خطای pidfd supervisor، ترتیب آزادکردن lock در
پنجرهٔ fail-closed، پوشش second-run stopping و still-absolute «تا خروج» در
متن مالکیت). اصلاحات دور دوازدهم طول عمر lock را به عملیات عادی محدود می‌کند،
ترتیب آزادسازی fail-closed را در همهٔ متن‌ها یکسان می‌کند و شروع مجاز پس از
آزادشدن `app.lock` را به تست R13-B اضافه می‌کند (commit `ee28f23`). بازبینی
متمرکز دوازدهم **Ready** بود: هیچ یافتهٔ باز مادی در §۷.۵، architecture §۷ یا
ردیف‌های R13 نماند؛ verdict فقط متن قرارداد/معماری/پلن را پوشش می‌دهد، نه اجرای
R13.

**R13-A انجام شد (۲۰۲۶-۱۱-۲۳):** `deedles.dev/tray v0.2.0`
(`acf85aba49de5147897736198745658a5ed8d157`، MIT) برای R13-B انتخاب و pin شد؛
`gogpu/systray v0.3.0` گزینهٔ جایگزین مستند است (registration ناموفق را فقط warn
می‌کند، `Id` را نادیده می‌گیرد و `Remove()` دوم panic می‌کند). شواهد: `spike/tray/`
(`FINDINGS.md`، `evidence/`، prototypeها؛ commit `77a5d22`) و snapshot تاریخ‌دار در
`research.md`. build استاتیک `CGO_ENABLED=0` برای چهار معماری، اجرای واقعی Plasma
6.6.7 (registration، icon ۲۲×۲۲، dbusmenu، menu event، notification)، تشخیص
no-watcher (exit 3) و watcher-without-host (exit 5)، و prototype pidfd برای
parent-death (child در ۱۴–۱۹ms؛ حالت arm-late ۴۵۳ms) گذشتند. اپ باید probe
watcher/host و notification را خودش داشته باشد (deedles notifications ندارد) و
`New()` را در حلقهٔ retry نکند (نشت fd). **آزمون‌نشده:** تأیید بصری آیکن/منو، GNOME
بدون extension/SSH واقعی، runtime `riscv64`/`ppc64le`، `ENOSYS`/`EPERM` pidfd، و
زنده‌ماندن پس از restart واقعی watcher.

**R13-B انجام شد (۲۰۲۶-۱۱-۲۳):** در سه commit: `332fa97` (فاز ۱: `run --headless`،
InstanceDir canonical، lockهای legacy پنجرهٔ migration، و `status.ready`/
`listeners_open`)، `ab78768` (فاز ۲: launcher detached/supervisor،
`app.lock`/`app.owner`/marker، tray با `deedles.dev/tray`، pidfd، readiness/exit،
منوی دو حالت) و `3a141a2` (رفع بازبینی: کانال کنترل per-generation tray، context
لغوپذیر، log واحد، ترتیب engine-lock/tray، سخت‌سازی استثنای engine، و تست‌های
منفی R13-B). بازبینی مستقل اول **Not ready** داد (نقص tray generation + تست‌های
غایب + متن‌های کهنه)؛ پس از اصلاح، بازبینی متمرکز **Ready** بود. checks:
`gofmt`/`go vet`/`go build` سبز، `go test -count=1 ./...` همهٔ پکیج‌ها سبز
(acceptance ~۱۱۸s)، `go test -race -count=1 ./internal/app` سبز،
`go mod verify` سبز و `scripts/check-artifact.sh` PASS. **آزمون‌نشده:** tray
واقعی KDE/DBus (تست‌ها fake؛ شاهد library در R13-A)، بستن ترمینال واقعی،
`ENOSYS`/`EPERM` pidfd روی kernel واقعی، runtime غیر-amd64، و failure-injection
مستقیم مسیر repair سه‌شکست.

**R13-C انجام شد (۲۰۲۶-۱۱-۲۳):** `autostart enable|disable|status` و helper
`internal/systemd` در `c113dde` پیاده شد و در `bd16367` پس از بازبینی اصلاح شد
(احراز/Hello اتصال private no-autolaunch، probe دارای deadline در
supervisor/autostart/doctor، حذف نشت goroutine، رد `--data-dir` با exit 2
به‌عنوان default-instance-only، exit 6 برای مسیر ناامن در status، و عدم chmod
دایرکتوری موجود). بازبینی مستقل اول **Not ready** (hang واقعی `ProbeTray`) و پس
از اصلاح **Ready** بود. checks: `gofmt`/`go vet`/`go build`، full tests،
`scripts/check-artifact.sh`، و تست گیت‌شدهٔ واقعی KDE با XDG موقت **PASS** (real
`~/.config/autostart` و سرویس میزبان دست‌نخورده). **آزمون‌نشده:** تست مالکیت
root (skip چون non-root)، رفتار `disable`/`status` علیه user manager واقعی، و
`umask` غیرعادی.

**R13-D انجام شد (۲۰۲۶-۱۱-۲۴):** `service install|uninstall|status` و helper
`internal/service` در `ea18f31` پیاده شد: unit دقیق §۷.۵ در base همان user
manager، pin کردن baseهای XDG یا `--data-dir`، quoting systemd،
idempotent/force/adoption، تعارض autostart/engine، readiness واقعی
(`status.ready`/`listeners_open`)، نگاشت exitهای child، snapshot/rollback با
precedence، linger hint بدون sudo، refuse کاربر دیگر، و uninstall
ownership-first. بازبینی مستقل یافته‌های مادی (از جمله هویت argv دقیق و تست
prefix-collision) داد که رفع و **Ready** شد. checks: `gofmt`/`go vet`/`go
build` (همراه `CGO_ENABLED=0`)، full tests (acceptance ~۱۱۹s)، `-race` روی
service/cmd، `scripts/check-artifact.sh`، و `systemd-analyze verify` روی unit
موقت. **آزمون‌نشده:** نصب/stop/restart/rollback/linger واقعی user manager
(سرویس زندهٔ میزبان دست‌نخورده)، branchهای PID/starttime فقط unit-tested، و
گیت‌شدهٔ status واقعی پیش‌فرض skip است.

**R13-E انجام شد (۲۰۲۶-۱۱-۲۴):** در `e6d0100`، unit از tarball/`.deb`/RPM/CI/
checker حذف شد و فایل منبع `deploy/systemd/routelatch.service` هم حذف شد؛
`README`، `packaging/README` (INSTALL)، `deploy/README` (SERVICE)،
`CONTRIBUTING`، `internal/acceptance/README` و `docs/README` مدل اجرای D26
(app/tray، `--headless`، autostart پیش‌فرض‌محور، `service install`، pidfd/
seccomp و migration از unit قدیمی) را توصیف می‌کنند. تست lifecycle قدیمی
systemd بازنشسته شد، corrupt-state به `run --headless` آفلاین تبدیل شد، و یک
تست migration برای unit تاریخی R12 از مسیر `service install --force` اضافه
شد. checks: full tests با **۲۷۴ PASS / ۰ FAIL / ۱۵ NOTE** (~۱۲۰s)، build
چهارمعماری و `package.sh`/`check-package.sh` در `DIST` ایزوله،
`check-artifact.sh`، `sh -n`، YAML parse، و grep بدون `deploy/systemd` در
مستندات عمومی. **آزمون‌نشده:** `service install`/uninstall/rollback/linger
و autostart واقعی در login (فقط fake-manager و status خواندنی)؛ سلول
default+data-dir به‌خاطر engine زندهٔ R12 در host NOTE می‌شود؛
`docs/release-readiness.md` snapshot تاریخی می‌ماند.

**R13-F انجام شد (۲۰۲۶-۱۱-۲۴):** بازبینی مستقل کل `9f678bb..1ae5f6a`
**Ready with findings** داد؛ دو یافتهٔ کد (نگاشت خطای tray، بازسازی marker
بدون probe) و ناسازگاری مستندات رفع شد و بازبینی متمرکز **Ready** شد. checks:
full tests **۲۷۴ PASS / ۰ FAIL / ۱۵ NOTE** (~۱۲۰s)، `gofmt`/`go vet`/
`go build` و build استاتیک، `-race` روی app/service/cmd، package چهارمعماری و
`check-package.sh`/`check-artifact.sh`، smoke واقعی KDE app/tray (readiness،
tray registration، SIGHUP/بستن pty، SIGTERM/SIGKILL بدون orphan)، و
گیت‌شدهٔ autostart واقعی PASS؛ tarball/deb/spec بدون unit/autostart/postinst.
**آزمون‌نشده (نیازمند مجوز/محیط):** نصب/rollback/linger واقعی user manager و
upgrade unit قدیمی `/usr/lib`، کلیک Quit در tray، GNOME/SSH واقعی، pidfd
`ENOSYS`/`EPERM`، runtime غیر-amd64، و سلول default+data-dir (NOTE به‌خاطر
engine زندهٔ R12). **انتشار:** در واحد جدا با مجوز کاربر انجام شد (پایین).

**انتشار v1.0.0 انجام شد (۲۰۲۶-۱۱-۲۴):** تگ annotated `v1.0.0` (شیء
`e6515c47d47f28510b942d7e2fc7e662bc82cc3c`) روی `782e243` ساخته و به هر دو
remote push شد. GitHub Release عمومی id `456148592` است:
<https://github.com/m-hosseinpour/routelatch/releases/tag/v1.0.0>. **۱۷ دارایی**
(چهار tarball، چهار `.deb`، چهار اسپک، چهار RPM و `SHA256SUMS`؛ SHA-256 برابر
`5b6964a1…aed7`) از `git archive 782e243` با `VERSION=v1.0.0`،
`COMMIT=782e243` و `SOURCE_DATE_EPOCH=1700000000` ساخته شدند؛
`check-artifact`/`check-package` pass، packaging دوم بایتیکسان، RPMها در
container یک‌بارمصرف rpm 6.0.1 ساخته/بازرسی و payload x86_64 اجرا شد؛ CI سبز
`45293072576` روی `782e243` دروازه بود. جزئیات و digestها در
`docs/release-readiness.md`. **آزمون‌نشده:** نصب/rollback/linger واقعی service،
upgrade unit قدیمی `/usr/lib`، کلیک Quit، GNOME/SSH، pidfd
`ENOSYS`/`EPERM`، runtime غیر-amd64 و default+data-dir.

**هدف و معیار:** A13/A15 — `run --headless` به‌عنوان engine foreground پایدار بماند؛
`run` پیش‌فرض app/tray باشد، بعد از readiness برگردد و با بستن ترمینال متوقف نشود؛
`autostart` و `service install` idempotent باشند؛ بسته هیچ unit/postinst نداشته باشد؛
نبود tray صریح fail شود.

**مرز و ریسک:** این تغییر behavior/نصب/قرارداد عمومی را عوض می‌کند و در صورت انتشار
breaking است (نه patch). تا پایان R13 هیچ release/tag انجام نمی‌شود. اجرای واقعی
tray/autostart/service روی میزبان و نصب/فعال‌سازی سرویس مجوز جدا می‌خواهد.

| ID | task | نتیجه | وابستگی | check اصلی |
| --- | --- | --- | --- | --- |
| R13-A | **spike tray pure-Go** | یک library/implementation برای StatusNotifierItem انتخاب شود که static `CGO_ENABLED=0` بماند و روی KDE آیکن/منو/نوتیفیکیشن کار کند؛ نبود tray روی GNOME بدون extension/SSH قابل تشخیص باشد؛ parent-death/child lifetime قابل پیاده‌سازی باشد | — | build static چهار معماری؛ اجرای واقعی KDE؛ بررسی license/deps/version؛ تشخیص نبود watcher؛ parent-death prototype |
| R13-B | **runtime `run`/`--headless`** | `run --headless` رفتار foreground فعلی را حفظ کند؛ `run` پیش‌فرض launcher detached، readiness، tray supervisor و child engine باشد؛ `InstanceDir` canonical زیر state/data (state.lock/app.lock/app.owner/socket، مستقل از XDG_RUNTIME_DIR)، flock `app.lock` + flock دوم `app.owner` (no-follow، inode پایدار، هرگز unlink، supervisor owner در عملیات عادی؛ در fail-closed ابتدا `app.owner` آزاد و سپس `app.lock` پس از توقف child/tray، CLOEXEC برای child)، marker `starting/ready/stopping` با invalidate قبل از flock، رکورد کامل/`data_dir`، transitionهای readiness و negative socket veto، repair/fail-closed (بدون socket-only already_running)، singleton per data-dir، lockهای legacy **همان data-dir** در پنجرهٔ migration، startup-in-progress → exit `6`، Quit/مرگ supervisor → توقف child، readiness deadline با teardown محدود به supervisor/فرزندان خودی (exit `8` فقط پس از مرگ تأییدشده؛ survivor/identity → `1`؛ pidfd unavailable → `7`)، `status.ready`/`listeners_open` mode-aware، لاگ/نوتیفیکیشن/`--json`، menu دو حالت؛ restart فقط با child جدید | R13-A | CLI tests؛ fake tray/child؛ بستن ترمینال؛ second-run ready/starting/stopping/stale-marker/lock-liveness/never-0-on-stale؛ app-vs-headless **صرف‌نظر از marker**؛ data-dir متفاوت؛ PID reuse/concurrent/corrupt/symlink/foreign marker؛ marker truncate/rewrite/newline/partial-read/repair-reflock-after-unlink/startup-phase-repair/repair-failure/engine-death/Quit/tray-loss stale-ready/deleted-marker-stopping/stopping-write-failure؛ lock delete/recreate؛ XDG_RUNTIME_DIR متفاوت (desktop/SSH)؛ legacy lock فعال/stopped با و بدون runtime و فقط برای همان data-dir؛ دو data-dir هم‌زمان؛ default+data-dir؛ runtime سفارشی مستند؛ stopped mode بدون listener؛ readiness timeout + concurrent-run + pidfd preflight (ENOSYS/EPERM) + supervisor-pidfd-open-failure (SIGKILL direct child, no reused PID) + child-pidfd-failure cleanup + signal-ESRCH/EPERM + child-death-wait/group-identity + fail-closed window (app.lock held + app.owner free → 6؛ پس از آزادشدن app.lock شروع مجاز) + no-orphan؛ port/corrupt/state؛ SIGKILL supervisor/child؛ `check-artifact.sh` |
| R13-C | **autostart** | `autostart enable\|disable\|status` با `$XDG_CONFIG_HOME/autostart`، keyهای کامل `.desktop` و quoting، idempotent، managed/stale/foreign، no-follow/regular/uid برای مسیر، بررسی tray host و pidfd مجاز (R-21)، `tray_available`/`pidfd_available` در status و doctor، منع هم‌زمان با service و `conflict` در status؛ فقط instance پیش‌فرض (رد `--data-dir` با exit 2) | R13-B | temp XDG؛ رد `--data-dir`؛ محتوای فایل؛ re-run no-op؛ stale/foreign/symlink conflict؛ tray-unavailable؛ pidfd-unavailable (ENOSYS/EPERM)؛ service-coexistence/status conflict؛ اجرای واقعی KDE (گیت‌شده) |
| R13-D | **service** | `service install\|uninstall\|status` با systemd user unit، `run --headless`، restart policy (`RestartPreventExitStatus=1 4 6 7 9 10`)، readiness واقعی با `status.ready`/`listeners_open` (listener فقط در mode غیر-stopped، نه فقط active؛ timeout exit `8`)، resolve/pin کردن **baseهای** XDG config/state در unit (Environment یا `--data-dir`) و نوشتن unit در base config همان user manager، linger hint، بدون sudo، `--force` با start/restart/adopt فقط پس از inactive، marker مالکیت، preflight unit خودش با attribution lockهای legacy خودش، snapshot/rollback با precedence (previous-active + MainPID/starttime یکسان، نصب تازه، inactive/adoption، restart/start failure)، exit mapping `1→1` و `4→6`/`6→6`/`7→7`/`9→9`/`10→10` (همه terminal)، quoting ExecStart/Environment، no-follow unit، `uninstall` اول ownership | R13-B | fake systemctl/loginctl/show-environment؛ temp XDG؛ unit content/marker/quoting/pinned base env/unit path (manager vs shell base)؛ second-run no-op؛ differing/foreign/symlink unit؛ force active/inactive/disabled/adoption (enabled-inactive و disabled-inactive)؛ own-service active با legacy locks؛ app/other headless refuse؛ stopped/timeout/readiness-before-bind/port/corrupt/unsupported/child-exit 1/4/6/7/9/10؛ rollback failed-restart-after-stop/active-disabled/fresh/adoption؛ autostart conflict/status؛ foreign uninstall بدون تغییر؛ gated real user manager |
| R13-E | **packaging/docs/migration** | حذف unit از tarball/`.deb`/RPM/CI/checker؛ به‌روزرسانی README، `packaging/README.md` (INSTALL)، `deploy/README.md` (SERVICE)، `CONTRIBUTING.md` و `internal/acceptance/README.md`؛ ثبت مرز pidfd مجاز (R-21) و جایگزین `--headless`/service در READMEها؛ migration از unit قدیمی (tarball و `/usr/lib`) و از lock/socket legacy R12 (default: `<StateDir>` + runtimeهای شناخته‌شده؛ `--data-dir`: فقط همان ریشه) به `InstanceDir` با نگه‌داشتن lockهای legacy برای یک نسخه؛ icon/desktop assets در صورت نیاز؛ rlch حفظ شود | R13-C/R13-D | `scripts/package.sh`/`check-package.sh`؛ بازرسی tarball/deb/spec؛ stale-unit upgrade با نتیجهٔ «unit قدیمی stop/disabled، بدون loop/orphan/direct، مسیر جدید سالم»؛ engine قدیمی active و stopped با/بدون `XDG_RUNTIME_DIR` → exit `4`؛ دو data-dir هم‌زمان و default+data-dir بدون lock سراسری نادرست؛ duplicate path/inode dedupe با Lstat/fstat بدون follow؛ symlink lock رد شود؛ default-old + data-dir-new صریحاً stop؛ `XDG_RUNTIME_DIR` سفارشی مستند و در migration stop صریح؛ desktop/SSH با runtime متفاوت یک lock؛ اسکریپت‌ها/تست‌ها به `run --headless`؛ README/INSTALL/SERVICE شامل pidfd/Linux ≥5.3/seccomp و جایگزین headless؛ CI |
| R13-F | **acceptance/review/release** | A13/A15 و harness؛ بازنشستگی تست‌های systemd-first یا تبدیل آن‌ها به service opt-in؛ بازبینی مستقل؛ تصمیم انتشار جدا | R13-E | `gofmt`/`go vet`/`go build`؛ `go test -count=1 ./...`؛ تست‌های گیت‌شده؛ package checks؛ verdict بازبین |

**ترتیب:** R13-A اول؛ سپس B. پس از B، C و D می‌توانند مستقل/موازی باشند. E بعد از
C/D و F بعد از E. B1 (افزودن `run --headless` و مهاجرت تست‌ها/اسکریپت‌ها) و B2
(تغییر پیش‌فرض به app/tray) در یک batch و بدون release میانی انجام شوند.

**تصمیم‌های قطعی:** user unit + linger برای VPS؛ `autostart enable` وجود دارد؛
`tray`/`gui` فرمان جدا نیستند؛ tray در حالت پیش‌فرض اجباری است و fallback بی‌صدا
ندارد؛ service فرمان opt-in است و بسته unit ندارد؛ نبود tray `unsupported`/`7` +
دلیل `tray_unavailable` است؛ `service install --force` unit را update و enable
می‌کند و اگر قبلاً active بود restart و اگر inactive بود start می‌کند؛ اجرای service
با `SUDO_USER`/`PKEXEC_UID` برای کاربر دیگر refuse می‌شود. invariant سخت
«یک engine در هر data directory» است، نه هر کاربر؛ app-mode flock `app.lock` را
نگه می‌دارد و flock دوم روی `app.owner` (inode پایدار) دارد؛ marker
`starting/ready/stopping` دادهٔ already_running است و فقط وقتی `ready` معتبر است
که flock آن در اختیار supervisor زندهٔ همان app.lock باشد و یک negative socket
check آن را رد نکند؛ transitionهای restart/Quit/tray-loss ابتدا `starting`/
`stopping` می‌نویسند؛ singleton از `app.lock` و readiness از `app.owner` می‌آید،
نه PID؛ startup در حال اجرا exit `6` می‌دهد، نه موفقیت کاذب؛ Quit/مرگ supervisor
child را متوقف می‌کند؛
مسیرهای XDG (`$XDG_CONFIG_HOME` با fallback `~/.config`) مبنا هستند؛ `autostart` و
`service` برای یک کاربر هم‌زمان فعال نمی‌شوند؛ unit/autostart مالک با marker از
غیرمالک جدا می‌شوند. `InstanceDir` canonical از state/data (`<StateDir>/run` یا
`<data-dir>/run`) می‌آید، نه `XDG_RUNTIME_DIR`؛ liveness app با flock روی
`InstanceDir/app.lock` است و supervisor مالک آن است (child با CLOEXEC آن را به
ارث نمی‌برد)، نه PID؛ `app.owner` هم با flock دوم و نوشتن در همان inode است و
در عملیات عادی تا خروج نگه داشته می‌شود؛ در پنجرهٔ fail-closed `app.owner` اول
آزاد و `app.lock` تا توقف child/tray نگه داشته می‌شود، و `app.owner` آزاد
به‌تنهایی هرگز مجوز شروع/`0` نیست. قواعد stale/corrupt/symlink در §۷.۵ آمده‌اند. پنجرهٔ migration R13 فقط lockهای
legacy همان data-dir را probe/نگه می‌دارد (برای `--data-dir` هیچ lock سراسری
runtime نه) تا engine قدیمی دوم (حتی stopped) ممکن نباشد و instanceها مستقل
بمانند. `service install` unit خودش را در preflight مجاز می‌شمارد و lockهای legacy
همان unit را تعارض نمی‌داند، unit/فایل غیرمالک را فقط با `--force` (regular
file/مالکیت و inactive) adopt می‌کند، baseهای XDG config/state را در unit pin
می‌کند و unit را در مسیر user manager می‌نویسد، readiness را از
`status.ready`/`listeners_open` (با stopped معتبر و timeout exit `8`) می‌سنجد،
exitهای `1/4/6/7/9/10` child را terminal map می‌کند، و در هر شکست پس از نوشتن
(شامل start/restart) با snapshot و precedence `MainPID`/starttime محتوا و
enabled/active را restore می‌کند؛ `uninstall` اول ownership/no-follow را می‌سنجد.

**تصمیم‌های باز تا R13-F:** آزمون واقعی user manager/linger روی توزیع‌های
مختلف؛ مرز GUI آینده؛ و تصمیم release/tag جدا. انتخاب/pin library tray و
اجرای R13-B/C/D/E بسته شدند.

**قدم بعدی:** هیچ کار پیاده‌سازی یا انتشار باز v1 نمانده است. آزمون‌های واقعی
user manager/linger، upgrade unit قدیمی، کلیک Quit و پلتفرم‌های غیر-amd64 فقط
با مجوز/محیط جدا انجام می‌شوند.

### وضعیت فعلی

- **R15-A — انجام شد (۲۰۲۶-۱۱-۲۵):** menu spike (`spike/tray/cmd/menu-spike`) روی
  Plasma 6.6.7/X11 و bus ایزوله آزموده شد؛ زیرمنو/separator، radio/checkmark،
  visible/enabled، add/remove/relabel **در منوی باز**، صفحهٔ ۲۰آیتمی از ۱۷۶ و
  label بلند/Unicode کار می‌کنند؛ eventها/زمان‌ها و محدودیت host ثبت شد. verdict:
  R15-D1 به live apply بسنده می‌کند (reopen-only لازم نیست) و باید صفحه‌گردانی را
  دسته‌بندی کند. جزئیات/شاهد: `spike/tray/MENU-FINDINGS.md` و
  `docs/research.md`. هیچ کد محصول/سرویس/تنظیم میزبان تغییر نکرد.

- **R15-B0 — انجام شد (۲۰۲۶-۱۱-۲۵):** `status.resume_requires_restart` به‌عنوان
  فیلد additive در `StatusData` اضافه شد؛ فقط stopped-by-process مقدار true
  می‌دهد، process تازه در stopped و حالت‌های targeted/all-proxy مقدار false صریح،
  payload قدیمی در مشتری unknown می‌ماند و `schema_version` تغییر نمی‌کند؛ هشدار
  `status` بین دو حالت stopped متفاوت است. `docs/contracts.md §۷.۱`، تست‌های
  focused و full tests سبز (acceptance ۱۳۰٫۱s)؛ بازبینی مستقل **Ready** (nit
  رفع‌شده).

- **R15-B — انجام شد (۲۰۲۶-۱۱-۲۵):** بستهٔ `internal/trayui` به‌عنوان
  model/controller خالص (بدون DBus) اضافه شد: snapshot تایپ‌شده، درخت منو با key
  پایدار، enum بستهٔ actionها، `Caller`/`Lifecycle`/`Notifier`، confirmation
  دو مرحله‌ای با expiry، یک job slot، refresh با fingerprint، sanitize/truncate و
  حالت stopped مبتنی بر `resume_requires_restart`. تست‌های fake caller/control
  server، `OK=false`، fixture ۱۷۶ pool/۱۱۷ rule، label مخرب و race سبز؛ `go vet`/
  `go build`/full tests سبز (یک اجرای acceptance flaked و rerun فوری سبز). بازبینی
  مستقل پس از C انجام شد.

- **R15-C — انجام شد (۲۰۲۶-۱۱-۲۵):** `restart-engine` سطح process در supervisor
  پیاده شد: کانال دوم per-tray-generation برای `restarting`/`ready`، پذیرش فرمان
  فقط از pipe کنترل همان-uid tray، marker `starting` قبل از teardown، teardown
  کامل child با pidfd و spawn child جدید، readiness با generation + `status.ready`
  + listener mode-aware + همان tray، سپس `ready`؛ رد `ready` کهنه با
  generation/token/pipe تازه. شکست‌ها fail-closed با exit 1/7/8 و هرگز `ready`
  کاذب؛ restart دستی budget خودکار را مصرف نمی‌کند و quit/lost/error سریال
  می‌مانند. `internal/app` focused/race/full tests سبز و بازبینی lifecycle مستقل
  **Ready**. adapter واقعی deedles در R15-D2 می‌آید.

- **R15-D1 — انجام شد (۲۰۲۶-۱۱-۲۵):** `Backend`/`Binder`/`Runner` در
  `internal/trayui` اضافه شد: apply دسته‌ای idempotent با key پایدار، add/remove/
  move/update، rebuild پس از خطا، runner با cadenceها و رویداد lifecycle، worker
  غیرمسدودکنندهٔ کلیک و shutdown تمیز؛ بدون DBus. تست‌های recording sink، fake
  clock، race، `-count=20`/`-race -count=5` و leak سبز؛ full tests (acceptance
  ۱۱۶٫۹s) سبز؛ بازبینی مستقل **Ready** (یک panic retry رفع شد). R15-D2 قدم بعد است.

- **R15-D2 — انجام شد (۲۰۲۶-۱۱-۲۶):** منوی ثابت با controller/runner و adapter
  واقعی deedles جایگزین شد: `TrayOptions.Socket/DataDir`، identity مسیر کامل،
  Apply دسته‌ای، lifecycle `quit`/`restart-engine` و `restarting`/`ready` ordered با
  رد token کهنه، Notifier coalesce/bounded و `ready` پس از registration. KDE gated
  با data-dir ایزوله radio/stop/restart/all-direct/`kill -9`/Quit را ثبت کرد؛
  full/race tests و build چهار معماری سبز؛ بازبینی مستقل **Ready** پس از رفع
  collision کلید تودرتو. آزمون‌نشده: Wayland/GNOME، اسکرین‌شات، desktop proxy
  واقعی و actionهای pool/rules/proposals. **CI:** commit `97aa8bc` در job
  `Test` قرمز شد؛ commit بعدی `dd2f216` (همان اصلاح runner) سبز شد. log
  GitHub بدون دسترسی خوانده نشد، پس علت دقیق تأیید نشده است.

- **R15-E — انجام شد (۲۰۲۶-۱۱-۲۶):** منوی pool audit شد؛ ترتیب `Test selected
  exit` و label سقف بر پایهٔ `contract.MaxPoolTestExits` اصلاح شد؛ تست‌های
  ۱۷۶رکورد/groups/pagination/gating/confirmation/job cap/legacy `state_locked`/
  sanitize/race اضافه شدند. rerank مطابق §۷.۳ sync است (متن ردیف اصلاح شد).
  full tests سبز (acceptance ۱۱۸٫۰s).

- **R15-F — انجام شد (۲۰۲۶-۱۱-۲۶):** rules/proposals audit شد؛ Recheck برای
  unroute proposal در `allowedLocked` رد می‌شود و override منو حذف شد. تست‌های
  ۱۱۷ rule در ۶ صفحه، remove confirmation، pending/decided/expired، accept
  route/unroute، reject/recheck/clear، stale/expired confirm، malicious label،
  D21 scan و race اضافه/سبز شدند؛ full tests (acceptance ۱۱۷٫۷s) سبز.

- **R15-G1 — انجام شد (۲۰۲۶-۱۱-۲۶):** desktop/diagnostics/data/settings audit شد و
  چهار gap رفع شد: Refresh در unsupported غیرفعال، نتیجهٔ restore برای no-op/
  user-modified صریح، notice به‌صورت ردیف disabled، و توضیح conflict=service.
  تست‌های fake desktop/autostart، set/restore confirmation، custom data-dir،
  doctor/log path، backup، D21 و no-fs-import سبز؛ full tests (acceptance
  ۱۱۷٫۹s) سبز.

- **R15-G2 — انجام شد (۲۰۲۶-۱۱-۲۶):** Terminal menu با argv صریح
  (`konsole -e`/`gnome-terminal --`/`xterm -e` + `menu`)، `Setsid`، بدون
  shell/$TERMINAL و fd sealing واقعی شد؛ terminal گم‌شده پیام sanitized و بدون
  request می‌دهد. Quit applied/unknown/restore-failure/keep/job و Last action
  پوشش داده شد؛ fake terminal/D21/race/full tests سبز. بازبینی مستقل کل سطح
  action انجام شد (Ready with findings) و پنج یافته در batch اصلاح و تأیید
  شدند. R15-H پذیرش نهایی را انجام داد و `v1.1.0` در ۲۰۲۶-۱۱-۲۶ منتشر شد.

- **R15-H — انجام شد (۲۰۲۶-۱۱-۲۶):** acceptance روی data-dir ایزوله/Plasma/X11
  و full checks (tests/race/build/package/check-artifact) سبز؛ A16 matrix با
  شاهد D-Bus و D21/no-secret ثبت شد. یک race واقعی در binder (label پیش از
  click target، ۳/۲۰۰) پیدا و با انتشار target پیش از Apply و regression
  deterministic رفع شد. README/deploy/docs/contracts/architecture به‌روز شدند؛
  شاهد: `spike/tray/evidence/r15h-acceptance-2026-11-26.md` و
  `r15h-2026-11-26/`. بازبینی نهایی مستقل **Ready with findings** بود و شاهد
  گم‌شده/claim غیرآرشیوی اصلاح شد. آزمون‌نشده: Wayland/GNOME، اسکرین‌شات، desktop
  set/restore واقعی، autostart، proposal accept/recheck/clear، pool remove/test،
  non-amd64 runtime و service install. `v1.1.0` پس از این ردیف منتشر شد (تگ
  `7abe9704` روی `532c169`، `Release` `45800238447`، id `459257636`، ۱۷ دارایی).

- **R15-0 — انجام شد (۲۰۲۶-۱۱-۲۵، همان جلسه):** سطح کنترل tray با D28/A16
  و قرارداد `contracts.md §۷.۳/§۷.۵` تعریف شد: کلاینت نازک روی management
  socket، منوی grouped/paginated، confirmation دو مرحله‌ای، jobهای async،
  status/mode/proposals/rules/pool/desktop/diagnostics/backup/autostart/quit،
  `Terminal menu` برای عملیات متنی و `restart-engine` سطح process. **هیچ کد،
  تست، بسته، سرویس، autostart، تنظیم دسکتاپ یا tray واقعی تغییر نکرد.**
  HEAD هنگام تعریف `cff2884` بود و working tree پاک. self-review پلن انجام شد
  و verdict برای R15-A **Ready** بود. R15 پس از v1 است؛ R1–R14/v1.0.1 باز
  نمی‌شوند. قدم بعد در آن زمان R15-A بود؛ اکنون R15 بسته و `v1.1.0` منتشر شده است.

- **R14 — واحد (a) انجام شد (۲۰۲۶-۱۱-۲۴، ارکستریتور):** مسیر انتشار D27 در
  `.github/workflows/release.yml` پیاده شد: تگ annotated `v*` (یا branch دائمی
  `release-dryrun` برای dry-run) چهار tarball، چهار `.deb`، چهار spec، چهار RPM
  باینری و `SHA256SUMS` ۱۶ فایلی را در CI می‌سازد، digests آرشیوهای pin‌شدهٔ
  sing-box و ادعای `PACKAGE-NOTES` را می‌سنجد و فقط بعد از draft→upload→verify
  منتشر می‌کند. runهای `45397329232` و `45400769014`، پس از دور اصلاح `45422318159`
  و dry-run نهایی `45463989732` (روی `02e3226`، پس از اصلاح mode اجرایی و پیام
  شکست اسکریپت تأیید) سبز و `45399453678` (شکست کنترل‌شده) fail شد؛ هیچ Release عمومی
  تازه‌ای ساخته نشد (listing بدون احراز هویت draftها را نشان نمی‌دهد). دور اصلاح:
  guard وجود Release عمومی/draft را رد می‌کند، پاکسازی فقط draft همان run را پس
  از بازخوانی `.draft == true` حذف می‌کند، digest با polling محدود سنجیده می‌شود و
  `RELEASE_BUILD=1` بدون `GITHUB_ACTIONS=true` رد می‌شود؛ این مسیرها با stub
  آزموده شدند، نه روی API واقعی. blob نهایی
  `e723d83a83a9e86bf3c5c0d3e1ae7abf5fffd2c3`. بازبینی مستقل دور اول **Not ready**
  بود (خطر حذف Release عمومی در پاک‌سازی اجرای دوباره)، پس از رفع دور دوم
  **Ready with findings** و پس از رفع دو یافتهٔ جزئی، تأیید کوتاه بسته شد؛ branch
  با fast-forward روی `main` رسید (`0183e3f`، سپس همین commit مستندات) و به هر دو
  remote push شد.
  **آزمون‌نشده:** job انتشار و اولین تگ واقعی؛ gateهای tag/on-main/CI-green در
  dry-run اجرا نمی‌شوند. جزئیات در `docs/release-readiness.md`.
  **واحد (c) — انجام شد (۲۰۲۶-۱۱-۲۵):** انتشار `v1.0.1` (patch) با مجوز کاربر
  پس از سه تلاش و یک دور رفع، منتشر شد. تلاش اول: `CI` `45490941645` روی
  `fb969fb` سبز شد و تگ `v1.0.1` (شیء `da6307e`) push شد، ولی `Release`
  `45492100825` در gate annotated شکست خورد. تلاش دوم: تگ روی `5ebbf57` بازسازی
  شد (شیء `3b9a8620`) و `Release` `45502745805` (job `138829983146`) با پیام
  `tag v1.0.1 is not annotated` و `SHA: 5ebbf57...` شکست خورد؛ بدون Release.
  علت واقعی: `actions/checkout` با `fetch-depth: 0` با refspec
  `+<commit>:refs/tags/<tag>` تگ محلی را lightweight می‌کند، نه اینکه
  `github.sha` شیء تگ باشد. gate اصلاح شد: تگ واقعی از `origin` force-fetch
  می‌شود و نوع آن باید `tag` باشد؛ ۱۴/۱۴ سناریوی محلی pass. تلاش سوم روی
  `4121481` (تگ `cce34dcb`) از gate گذشت و `Release` `45510002182` (job انتشار
  `138857993885`) draft ساخت و ۱۷ دارایی را آپلود کرد، ولی verify با endpoint تگ
  برای draft (404) شکست خورد و cleanup هم draft را ندید (id `457460945`)؛
  یادداشت‌ها هم به‌جای پیام تگ، پیام commit را خواندند. رفع idمحور و رفع منبع
  یادداشت‌ها در `d14da10` با بازبینی مستقل **Ready with findings** (بدون یافتهٔ
  مسدودکننده) بسته شد. draft با مجوز کاربر حذف و نبود Release/draft با listing
  احرازشده تأیید شد؛ تگ قدیمی محلی و روی هر دو remote حذف و تگ annotated تازه
  `4462cc86` روی `d14da10` ساخته شد. `CI` `45517940277` روی همان commit هر شش
  job سبز بود و `Release` `45521099138` (job انتشار `138895476159`) نسخهٔ عمومی
  `v1.0.1` را با ۱۷ دارایی منتشر کرد. **R14 بسته است؛ هیچ کار باز v1 نمانده.**

- **R13 — R13-A..F بسته شدند (۲۰۲۶-۱۱-۲۳/۲۴، ارکستریتور):** D26 ثبت و
  A13/A15/§۷.۵ به‌روز شد؛ بازبینی متمرکز دوازدهم **Ready** بود. R13-A
  `deedles.dev/tray v0.2.0` (MIT) را pin کرد (`77a5d22`؛ Plasma 6.6.7، build
  استاتیک چهار معماری، pidfd parent-death؛ تأیید بصری/GNOME/SSH/runtime دو
  معماری آزمون‌نشده). R13-B در `332fa97`، `ab78768` و `3a141a2` پیاده شد:
  `run --headless` foreground، launcher/supervisor و tray پیش‌فرض، InstanceDir/
  lock/marker/pidfd/readiness و تست‌های جدول. R13-C در `c113dde` و `bd16367`
  پیاده شد: autostart، helper `internal/systemd`، probe احرازشده با deadline،
  رد `--data-dir`، و gated KDE واقعی **PASS**. R13-D در `ea18f31` پیاده شد:
  `service install/uninstall/status`، unit دقیق، base pinning، readiness،
  rollback، force/adoption و تعارض‌ها. R13-E در `e6d0100` unit را از
  بسته‌ها/CI/checker حذف و README/INSTALL/SERVICE/migration را به‌روز کرد
  (۲۷۴ PASS/۰ FAIL/۱۵ NOTE). R13-F بازبینی مستقل کل change را با
  **Ready with findings** گذراند؛ `2f1e60d` (tray error mapping و marker
  repair) و مستندات پس از آن **Ready** شدند. smoke واقعی KDE app/tray
  (readiness، tray registration، SIGHUP، SIGTERM/SIGKILL بدون orphan) و
  package checks چهارمعماری pass. **آزمون‌نشده:** نصب/rollback/linger واقعی
  user manager، upgrade unit قدیمی `/usr/lib`، کلیک Quit، GNOME/SSH، pidfd
  ENOSYS/EPERM، runtime غیر-amd64 و default+data-dir (NOTE). هیچ سرویس، unit،
  autostart یا تنظیم دسکتاپ تغییر نکرد. `v1.0.0` با ۱۷ دارایی منتشر شد
  (release id `456148592`؛ `e6515c4` روی `782e243`).

> **توجه به هشها:** تاریخچه در انتشار بازنویسی شد؛ هشهای کوتاهی که در ردیفهای پایین
> برای کارهای R1–R10 آمدهاند مربوط به قبل از بازنویسیاند و در مخزن عمومی وجود
> ندارند. مرجع معتبر، HEAD انتشارشده است.
>
> **یادداشت وضعیت جاری (۲۰۲۶-۱۱-۲۲):** هرجا در رکوردهای تاریخی زیر «wire-level replay
> باز/اثبات‌نشده» آمده، وضعیت جاری همان رکورد ۲۰۲۶-۱۱-۲۲ بالاست: این مورد روی مسیر
> واقعی pool با gate/مقصد تحت کنترل کاربر اثبات شد (یک مقصد/دو exit، بدون تعمیم).
> رکوردهای تاریخی بازنویسی نشده‌اند.
  **بستن چرخه و تصمیم انتشار — ۲۰۲۶-۱۱-۲۱ (ارکستریتور):** این چرخه بسته شد و
  `git status` پیش از commit همین سند پاک و HEAD `4367e14` بود. واحدهای بسته‌شده و
  commitها: `ccdb4df` (بازطبقه‌بندی ترنسپورت‌های پشتیبانی‌نشده)، `77fc0b6` (آزمون
  reuse طولانی‌مدت)، `645f5ed` (سلول‌های obfs هیستریا۲)، `cdb99bd` (اثبات sidecar
  منتشرشده)، `c2dad43` (corrupt-state زیر unit موقت)، `42b9e4e` (بهداشت مستندات و
  checker)، `15d2acb` (اعتبارسنجی import برای `missing_obfs_password`)، اجرای payload
  RPM روی amd64 در container یک‌بارمصرف (بدون نوشتن در repo)، `6f5b987` (README و
  snapshot)، `c5d48b4` (ادعاهای کهنهٔ RPM) و `4367e14` (status مشتق برای رکوردهای
  ذخیره‌شده). هر واحد کد/تست بازبینی مستقل را گذراند؛ آخرین اجرای ثبت‌شدهٔ
  `go test -count=1 ./...` **۱۷/۱۷** سبز است؛ و snapshot شاهد پذیرش **۲۲۲ PASS / ۱۳ NOTE / ۰ FAIL**
  متعلق به HEAD کد `15d2acb` است (سلول‌های گیت‌شده به شمارش پیش‌فرض اضافه نمی‌شوند).
  **تصمیم انتشار:** نسخهٔ `v0.1.3` برای همین batch در نظر گرفته شده است — patch، چون
  فقط اصلاح اعتبارسنجی import، اصلاح status مشتق، تست و مستندات دارد و قابلیت
  کاربرمحور، قرارداد عمومی یا فرمت دادهٔ ذخیره‌شده را تغییر نمی‌دهد. push، تگ و انتشار
  این batch در ۲۰۲۶-۱۱-۲۱ صریحاً توسط کاربر مجاز شد.
  **بازخوانی پیشین ۲۰۲۶-۱۱-۲۱ (ارکستریتور، پیش از بستن این چرخه):** `git status` پاک و HEAD `15d2acb`. این چرخه reuse طولانی‌مدت ترنسپورت (ب/۱)، hysteria2 obfs salamander/gecko و هر دو حالت منفی (ب/۲)، اثبات sidecar از بستهٔ منتشرشدهٔ v0.1.2 (ب/۴)، corrupt-state زیر unit موقت `systemd --user` (ب/۵)، رفع `missing_obfs_password` در import، بهداشت مستندات/checker و اجرای payload RPM روی amd64 (ردیف ۲۰۲۶-۱۱-۲۱) را بست. snapshot تازه روی همین HEAD: `gofmt -l` خالی، `go vet ./...` و `go build ./...` exit 0، `go test -count=1 ./...` سبز (۱۷/۱۷ پکیج، acceptance ۱۱۵٫۸s) و `go test -count=1 -v ./internal/acceptance/` → **۲۲۲ PASS، ۱۳ NOTE، ۰ FAIL** (۱۲۱٫۹s). عدد ۲۲۲ ردیف قدیمی مربوط به HEAD `db6a50f` است؛ این snapshot مستقل روی `15d2acb` است.
  **بازخوانی ۲۰۲۶-۱۱-۲۰ (ارکستریتور):** `git status` پاک، HEAD `75a7183` (یک commit صرفاً سندپسند بالای `db6a50f`) و باینری نصبشده `/usr/bin/routelatch` دقیقاً v0.1.2 / commit `88d04b1` (همان اصلاح re-apply دسکتاپ) است؛ sidecar `/usr/bin/sing-box` 1.14.4 (با `sing-box version`). در همین بازخوانی `systemctl --user is-enabled/is-active routelatch.service` هر دو `enabled`/`active` و `ExecMainStatus=0`/`NRestarts=0` گزارش کردند؛ `status` سالم (targeted/policy 117، pool ۱۷۶ رکورد / ۱۵۷ usable / ۱ healthy). مسیر `~/.local/bin` در یادداشت قدیمیتر مربوط به نصب پیشین است؛ مرجع فعلی `/usr/bin` است.
  (snapshot تاریخی ۲۰۲۶-۱۱-۱۹: HEAD پیش از R12 فاز ۲ `6a7580f` و commitهای فاز ۲ `3839d6a` + اصلاح `505a650`اند. بررسیها روی معادل قبل از بازنویسی `3425c3f` اجرا و روی `3cb7bd7` پس از بازنویسی تأیید شد —
هنگام شروع، `git log` را ببین)
   **بازخوانی سرد ۲۰۲۶-۱۱-۱۸ (ارکستریتور):** `git status` پاک و HEAD واقعی `b01661d` بود — دو commit صرفاً سندپسند (`4106da5` بستن ادعای کهنهٔ انتشار، `b01661d` ثبت R11) بالای `efc73c3` که رکورد به آن اشاره میکرد. تنها HEAD بهروز شد؛ هیچ کد/سرویس/تنظیم میزبان تغییر نکرد. commit بازخوانی: `a18dd97`.)

- **نام کوتاه `rlch` (۲۰۲۶-۱۱-۲۲، ارکستریتور؛ درخواست کاربر):** نام `rlch` به‌عنوان symlink نسبی به `routelatch` در tarball، `.deb` و اسپک RPM نصب می‌شود؛ `routelatch` نام اصلی و `ExecStart` می‌ماند. `rtlch` به‌دلیل پیشوند شلوغ `rtl` (rtl-sdr و معنای right-to-left) رد شد. تغییر فقط بسته‌بندی/مستندات است (بدون کد Go): `scripts/package.sh` لینک `bin/rlch -> routelatch` را می‌سازد، `.deb` آن را با `cp -a` حفظ می‌کند، اسپک `ln -s routelatch %{buildroot}%{_bindir}/rlch` و `%{_bindir}/rlch` در `%files` دارد؛ `scripts/check-package.sh` و CI وجود/هدف لینک را می‌سنجند. اسناد: D25، §۷.۱ قراردادها، README، packaging/README و deploy/README. **شواهد:** `sh -n` و YAML parse سبز؛ `scripts/package.sh` و `check-package.sh` روی کپی `dist` موجود (`ARCHES='amd64 arm64'`، v0.1.0، با sidecar) برای هر دو معماری **PASS**؛ `tar -tvzf` و `dpkg-deb --fsys-tarfile` لینک `lrwxrwxrwx ... rlch -> routelatch` را نشان دادند و `env -i .../bin/rlch version` از tarball و `.deb` استخراج‌شده exit 0 داد؛ اجرای دوم packaging همان `SHA256SUMS` را داد؛ تست منفی با جایگزینی لینک با کپی باینری با پیام `bin/rlch is not a symlink to routelatch` fail شد. **اجرا نشد:** build/تست Go (در PATH این محیط `go` موجود نیست؛ تغییر کد Go ندارد) و ساخت باینری RPM (rpmbuild نصب نیست)؛ CI این دو را پوشش می‌دهد. **بازبینی مستقل:** **Ready with nits** — بدون یافتهٔ مسدودکننده؛ بازبین بسته‌بندی دو معماری و تست‌های منفی (جایگزینی لینک با فایل معمولی و هدف مطلق) را تکرار کرد و شواهد متن را تأیید کرد؛ nitها (مسیر `bin/rlch` در متن، دقت `argv[0]`، ثبت شواهد نام در research، ابهام README و دقت ردیف پلن) اعمال شدند. **تحویل:** commit `e181733` به هر دو remote push شد؛ CI `45037281497` روی همان commit هر ۶ job را سبز کرد، شامل `rpm-packages` (ساخت واقعی RPM و بررسی symlink). **قدم بعدی:** انتشار/تگ تصمیم جدا.

- **گروه ج — D22 روی مسیر واقعی pool اثبات شد (۲۰۲۶-۱۱-۲۲، ارکستریتور؛ یک مقصد، دو exit، بدون تعمیم؛ بدون تغییر کد محصول):** instrument تحت کنترل کاربر روی یک VPS عمومی بالا بود: gate روی پورت عمومی، مقصد شمارنده روی loopback، `/_count/<token>` بدون increment، و اولین اتصال هر توکن پس از forward شدن به شمارنده بدون هیچ بایت پاسخ reset می‌شد؛ اتصال‌های بعدی همان توکن relay می‌شدند. این gate یک **مقصد** تحت کنترل است، نه exit؛ exitها از pool واقعی و سالم بودند. دسترس‌پذیری از بیرون `OK 0` در ~۹۳ms (تأیید هماهنگ‌کننده) و از همین میزبان هم `OK 0`. **نمونهٔ ایزوله:** data-dir زیر `/tmp/opencode/rl-d22-real/instance` (0700؛ فایل‌ها 0600) با portهای ۱۸۳۱۷/۱۸۳۱۸ و process foreground تحت setsid بدون unit؛ seed همان کپی فقط‌خواندنی `state.json` سرویس زنده (۱۷۶ رکورد) بود که source/rules/desktop در آن حذف و فقط یک rule `ip` برای IP عمومی instrument اضافه شد؛ mode `targeted` و policy ۱۲۷→۱۲۸؛ usable ۱۵۶. **آزمون اصلی (توکن تازه):** شمارنده پیش **۰**؛ یک CONNECT با `curl --proxytunnel` (بدون `--retry`، timeout ۶۰s) از proxy ایزوله: **rc=0**، `200 Connection Established`، سپس **دقیقاً یک** `HTTP/1.1 200 OK` با **۴ بایت** و بدنهٔ **`OK 2`** در **۲۱٫۰۲s**؛ شمارنده پس **۲** (بازبینی دوباره ۲). تلاش اول درخواست را به مقصد رساند (در اجرای تشخیصی، شمارنده پیش از retry **۱** بود) و تا ~۲۰٫۵s هیچ بایت پاسخ نداشت و `proofs.accepted` پیشرفت نکرد؛ سپس با kind **`destination_unconfirmed`** شکست خورد و cooling شد. retry به exit دوم در ~۰٫۵s همان بایت‌های بافرشده را طبق قرارداد §۲.۲ (نه مشاهدهٔ instrument) replay کرد و با اولین بایت پاسخ commit شد؛ instrument تعداد درخواست‌ها را شمرد، نه محتوای بایتی. پس مقصد همان درخواست را دو بار دید و `proofs.accepted` (نه تحویل بایت) یک واحد جلو رفت؛ `confirmed=0`. دو exit متمایز بودند (هر دو trojan/ws/tls)؛ اولی نمونهٔ شکست و دومی نمونهٔ egress مثبت گرفت. **اجرای تشخیصی قبلی با timeout ۲۰s (بدون `--retry`)** در لحظهٔ timeout **rc=28** و ۰ بایت و شمارنده ۱ داد، ولی ~۱s بعد شمارنده ۲ شد: replay رخ داده بود و فقط کلاینت زودتر رها کرده بود؛ با timeout بلندتر پاسخ رسید. **کنترل pin (توکن تازه):** همان exitی که در آزمون اصلی پاسخ داده بود به‌صورت دستی pin شد و `selected_exit` پس از شکست هم حفظ شد؛ یک CONNECT بدون retry: **rc=52** و «Empty reply from server» در **۲۰٫۰s**، فقط ۲۰۰ CONNECT و **۰ بایت بدنه**؛ شمارنده پیش **۰**، پس **۱** و بازبینی دوباره **۱**؛ `proofs.accepted` بدون تغییر (بدون commit)، فقط همان pin یک نمونهٔ `destination_unconfirmed` گرفت و cooling شد. یعنی همان exit/مقصد، توکن تازه و pin دستی؛ gate اولین اتصال آن توکن را reset کرد (در آزمون اصلی همان exit اتصال دوم بود و relay شد) و replay رخ نداد. **حدود:** یک مقصد/یک gate، دو exit واقعی (هر دو trojan/ws/tls)، یک ترتیب موفق + یک ترتیب تشخیصی و در مجموع سه CONNECT محصول (تشخیصی/اصلی/کنترل)؛ تأخیر ~۲۰s در آشکارشدن شکست pre-byte در همین دو exit دیده شد، علتش تعیین نشد و به بقیه تعمیم نمی‌یابد؛ شمارنده روی VPS کاربر است، نه این میزبان، و `gate.log`/`dest.log` در دسترس این واحد نبود، پس شاهد، count و رفتار کلاینت است (هماهنگ‌کننده می‌تواند لاگ را تأیید کند)؛ پنجره time-bounded نیست و کلاینتی که پیش از آشکارشدن شکست exit timeout کند پاسخ را نمی‌بیند — این با قرارداد §۲.۲ هم‌خوان است، ولی برای کلاینت‌های دارای timeout کوتاه یک محدودیت کاربرمحور است؛ به‌عنوان نقص محصول شناسایی نشد و دخالت محصول در این تأخیر رد نشده است. **ایزولاسیون/secrets:** سرویس زنده پیش و پس `active` با `MainPID=79606`، `NRestarts=0`، `ExecMainStatus=0` و listenerهای ۸۳۱۷/۸۳۱۸؛ پروکسی دسکتاپ دست نخورد؛ نمونهٔ ایزوله با portهای ۱۸۳۱۷/۱۸۳۱۸ و بدون unit، پس از آزمون خاموش و بدون listener/process؛ کپی seed، state/log ایزوله و tokenها حذف شدند؛ شاهد نگه‌داشته فقط aggregate و خروجی curl بدون لینک/UUID/credential/node host:port است. **HEAD پیش از commit: `9b1993f`؛ بدون تغییر کد محصول؛ در دامنهٔ این اندازه‌گیری (replay) نقصی دیده نشد؛ teardown/اتصال‌های باقی‌مانده پس از بستن کلاینت بررسی مستقل نشد.**

- **گروه ج — بخش ۳ انجام شد (۲۰۲۶-۱۱-۲۱، ارکستریتور؛ شکست TLS-pin روی یک عضو واقعی اثبات شد، بدون تعمیم؛ wire-level replay روی pool واقعی اثبات نشد):** طبق دامنهٔ همین واحد فقط دو مورد باقی‌مانده تعیین شد. **نمونهٔ ایزوله:** data-dir زیر `/tmp/opencode/rl-part3/instance` (0700؛ فایل‌ها 0600) با portهای ۱۸۳۱۷/۱۸۳۱۸ و process foreground بدون unit؛ seed همان کپی فقط‌خواندنی `state.json` سرویس زنده (۱۷۶ رکورد) بود که sourceهای حاوی origin و همهٔ rules در آن حذف و فقط یک rule دقیق برای مقصد آزمون اضافه شد. **pin:** یک عضو واقعی TLS pool (trojan/ws/tls) با افزودن `pinned_peer_cert_sha256` اشتباه (۶۴ hex صفر) و بدون تغییر هیچ فیلد دیگر انتخاب شد. یک CONNECT به مقصد تأییدشده از proxy ایزوله: curl با **rc=35** و «TLS connect error: unexpected eof» در **۰٫۲۵s**، **۰ بایت** پاسخ، **۰ درخواست در شمارندهٔ سمت مقصد** (سرویس ثبت درخواست عمومی؛ بازبینی دوباره پس از فاز کنترل هم ۰)، `proofs={accepted:0,confirmed:0}` و `open=0`، بدون fallback مستقیم؛ رکورد cooling شد و دو نمونهٔ شکست ثبت شد؛ مسیر CONNECT کاربر شکست pre-byte را با kind `destination_unconfirmed` ثبت می‌کند و reason نهایی **`cooling:exit_unreachable`** فقط از یک نمونهٔ probe همان رکورد می‌آید (probe هم pin را بررسی می‌کند؛ tick خودکار ۳۰s با gate چرخش ۱۰ دقیقه)، پس هر دو نمونه به یک CONNECT نسبت داده نمی‌شوند؛ `status` سرویس خوانا ماند. پس از شکست، pin حفظ می‌شود: مسیر شکست فقط health/status را می‌نویسد و هیچ کدی `pinned_peer_cert_sha256` را پاک یا `insecure_tls` را فعال نمی‌کند (تأیید منبع‌کدی؛ state ایزوله حذف شد؛ `internal/pool/health.go`، `internal/engine/config.go`). **کنترل در همان نمونه (همان data-dir/ports؛ process فقط برای بازگرداندن رکورد ری‌استارت شد):** پس از حذف pin، انتخاب دستی همان رکورد cooling پذیرفته شد و یک CONNECT به همان مقصد **200 با ۱۵۶ بایت** در **۰٫۹۰s** و **دقیقاً ۱ درخواست** در شمارندهٔ مقصد داد؛ `proofs.accepted=1`/`confirmed=0` و رکورد دوباره healthy شد. **wire-level replay:** شمارندهٔ سمت مقصد اکنون در دسترس است، ولی پنجرهٔ D22 به exitی نیاز دارد که درخواست را بپذیرد/forward کند و پیش از اولین بایت پاسخ بمیرد؛ exit عمومی ثالث را نمی‌توان دقیقاً در آن نقطه شکست داد و شکست pre-byte آن از این میزبان قابل مشاهده نیست. در مسیر core-backed، CONNECT محلی پیش از تماس با exit برمی‌گردد (proof class `accepted`) و شکست pin بعد از آن تونل را می‌بندد؛ exit هرگز درخواست را نپذیرفت و شمارندهٔ مقصد ۰ ماند، پس pin غلط پنجرهٔ replay را فعال نکرد. بنابراین نیمهٔ pool واقعی **اثبات‌نشده** ماند و اجرا نشد؛ شاهد fixture محلی بخش ۲ (exit اول ۱۳۹ بایت را forward کرد و origin درخواست ۱ را دید، exit دوم همان درخواست را دید و کلاینت فقط یک پاسخ گرفت) به‌عنوان کران بالای پنجره معتبر است. **قابلیت گمشده در آن تاریخ:** یک exit تحت کنترل ما (مثلاً VPS) در لبهٔ pool که درخواست نشان‌دار را forward کند و پیش از اولین بایت پاسخ قطع شود، به‌همراه مقصد شمارنده. **در ۲۰۲۶-۱۱-۲۲ این نیاز با یک gate/مقصد تحت کنترل (نه exit) و دو exit واقعی برآورده شد** (رکورد بالاتر)؛ واریانت «exit تحت کنترل» جداگانه آزموده نشد. **حدود:** فقط یک عضو TLS و یک تلاش pin آزموده شد و نتیجه به سایر exitها/پروتکل‌ها تعمیم نمی‌یابد؛ کنترل نیازمند ری‌استارت process بود (نه همان process)؛ شمارندهٔ مقصد یک سرویس ثالث است، نه میزبان ما؛ pin failure در سطح محصول `exit_unreachable` گزارش شد و خطای Xray عمداً لاگ نمی‌شود. **ایزولاسیون/secrets:** سرویس زنده پیش از آزمون و بلافاصله پس از خاموشی نمونه `active` با `MainPID=2026`، `NRestarts=0`، `ExecMainStatus=0` و listenerهای ۸۳۱۷/۸۳۱۸ بود؛ در بازبینی نهایی (۱۶:۴۲:۴۱) یک restart تمیز که این واحد صادر نکرد (`Stopping`→`Started`، بدون failure و با `NRestarts=0`) با `MainPID=79606` و باینری عادی `/usr/bin/routelatch` دیده شد، نه باینری/دادهٔ ایزوله؛ علت آن تعیین نشد و هیچ فرمان تغییردهنده‌ای از این واحد به unit زنده فرستاده نشد. هیچ listener/process آزمایشی نماند؛ اسکن پیش از حذف روی log و شاهد حفظ‌شده **۰** hit برای server/display/credential هر ۱۷۶ رکورد و **۰** URL/UUID داد (خروجی خام اسکن نگه داشته نشد؛ خلاصه در شاهد)؛ کپی seed، state/log ایزوله، بدنه‌ها و token مقصد حذف شدند. **HEAD پیش از commit: `d50db6f`؛ بدون تغییر کد محصول؛ نقص محصول یافت نشد.** این مورد در ۲۰۲۶-۱۱-۲۲ با gate/مقصد تحت کنترل کاربر اثبات شد (رکورد جدید بالاتر)؛ محدودیت‌های همان اثبات به قوت خود باقی است.

- **پس‌گرفتن ریلیز `v0.1.4` و اصلاح قاعدهٔ تحویل (۲۰۲۶-۱۱-۲۱، ارکستریتور):** batch شواهد گروه ج (`1be8e9f`، `79c0e53`) فقط push شد و ریلیز نمی‌شود. تگ annotated `v0.1.4` (شیء تگ `8e224a7`) و ریلیز عمومی GitHub آن (id `453655267`، ۱۵ دارایی از ۱۷ آپلود شده) در همان روز به درخواست کاربر پس‌گرفته شدند، چون ریلیزی که ارزش کاربری اضافه نمی‌کند نباید منتشر شود: ریلیز با API حذف شد (HTTP 204) و تگ محلی و روی هر دو remote حذف شد. **تأیید:** `git tag -l v0.1.4` خالی است، `git ls-remote --tags` روی `github` و `origin` هیچ `v0.1.4` ندارد، `main` هر دو remote روی `79c0e53` است و `v0.1.0`–`v0.1.3` دست‌نخورده‌اند؛ آخرین ریلیز در آن تاریخ همان `v0.1.3` بود. قاعدهٔ تحویل در `AGENTS.md` بازنویسی شد: push همچنان بدون درخواست جدا انجام می‌شود، ولی tag/release فقط برای batchی است که چیزی را که کاربر دریافت می‌کند تغییر می‌دهد (رفتار برنامه، قابلیت، بسته‌بندی/artifact، امنیت یا قرارداد کاربرمحور)؛ batch فقط‌مستند/شاهد push می‌شود و ریلیز نمی‌شود. جزئیات در `docs/release-readiness.md` است.

- **رد gecko بدون رمز obfs در import (۲۰۲۶-۱۱-۲۱، ارکستریتور — رفع یافتهٔ ب/۲):** لینک `hysteria2://…?obfs=gecko` بدون `obfs-password` (یا با مقدار خالی) حالا در import با status `invalid` و دلیل پایدار **`missing_obfs_password`** رد می‌شود؛ پیش از این `unknown` وارد می‌شد و sing-box v1.14.4 بعداً کانفیگ تولیدشده را با «missing obfs password» رد می‌کرد، خلاف invariant «اعتبارسنجی پیش از ساخت engine». تغییر در `internal/imports/record.go` (دروازهٔ `validateNode`) و ثابت/متن دلیل در `internal/imports/sanitize.go` است؛ `obfs` همچنان اختیاری است، salamander بدون رمز رفتار پیشین (`invalid_obfs`) را نگه می‌دارد و نوع ناشناخته هم `invalid_obfs` می‌ماند. همین دروازه برای outbound JSON هیستریا۲ هم کار می‌کند. قرارداد عمومی در `docs/contracts.md` §۵.۴ ثبت شد. **شواهد:** تست `TestHysteria2ObfsPasswordRequired` پیش از تغییر روی gecko بدون رمز `status=unknown` داد و پس از تغییر pass (شامل `hy2://`، alias `obfsPassword` و رمز خالی)؛ `gofmt -l` روی چهار فایل Go تغییریافته خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./internal/imports/...` سبز؛ `go test -count=1 ./...` سبز (۱۷/۱۷ پکیج، acceptance **۱۱۶٫۹۹۷s**، گیت‌ها خاموش). **بازبینی مستقل:** `Ready with nits` — یک نادرستی سند در توضیح مکانیسم دادهٔ قدیمی و دو nit (کامنت فیلد قرارداد، پوشش alias شامل حالت مثبت `obfsPassword`) رفع شدند؛ دور تأیید: **Ready** (تنها nit باقی‌مانده، مثبت‌کردن تست alias بود که همان‌جا اعمال شد). **باقی (که بعداً رفع شد — «تصمیم‌های باز» مورد ۱، ۲۰۲۶-۱۱-۲۱):** در آن زمان رکورد gecko بدون رمزی که پیش از این تغییر ذخیره شده بود در import جدید ساخته نمی‌شد؛ اگر در state قدیمی می‌ماند، دو validator آن دورهٔ engine/singbox (که رمز را فقط برای salamander اجباری می‌کردند) آن را نمی‌گرفتند و هنگام Sync کانفیگ sidecar، sing-box آن را رد می‌کرد و همهٔ exitهای hysteria2 با `sidecar_unavailable` کنار گذاشته می‌شدند — fail-closed و بدون direct، ولی بدون جداسازی per-record. رفع بعدی مشتق‌سازی `unsupported`/`missing_obfs_password`، کنارگذاری per-record و validator جدید gecko را اضافه کرد، بدون migration یا بازنویسی state. commit محلی: `fix(imports): reject gecko obfs without obfs-password`.

- **اجرای payload باینری RPM روی amd64 (۲۰۲۶-۱۱-۲۱، worker؛ ثبت توسط ارکستریتور):** یک RPM واقعی `x86_64` از HEAD `42b9e4e` در container یک‌بارمصرف `ubuntu:26.04` ساخته شد: داخل آن Go 1.27.4، `GOPROXY=off`، `scripts/build.sh` و `scripts/package.sh` با `ARCHES=amd64` (sidecar از `dist/singbox`، `REQUIRE_SINGBOX=1`) اجرا شد و سپس `rpmbuild` 6.0.1 اسپک تولیدشده را ساخت. خروجی `routelatch-0.1.2.8.g42b9e4e-1.x86_64.rpm` با Version `0.1.2.8.g42b9e4e`، Release `1`، Arch `x86_64`، اندازهٔ ۱۱۷٬۱۷۳٬۶۷۳ بایت و sha256 `e83afdf1…4c46da`؛ ۱۴ فایل شامل `/usr/bin/routelatch`، `/usr/bin/sing-box`، `LICENSE`/`NOTICE` در `/usr/share/licenses/routelatch/`، اسناد در `/usr/share/doc/routelatch/` و unit در `/usr/lib/systemd/user/routelatch.service` با `ExecStart=/usr/bin/routelatch run`. `rpm -qp --scripts` خالی است: بدون scriptlet، بدون فعال‌سازی سرویس و بدون هیچ ورودی `/etc/systemd`. **اجرای payload:** `bin/routelatch version` → `RouteLatch v0.1.2-8-g42b9e4e`، commit `42b9e4e`، exit 0؛ `--json version`، `--help` و `doctor` هر سه exit 0 (هشدارهای doctor فقط «سرویس در حال اجرا نیست» و «pool خالی»)؛ `bin/sing-box version` → 1.14.4 / `c005f353`، exit 0. `rpm -i --nodeps` exit 0؛ `/usr/bin/routelatch version` پس از نصب همان خروجی بود؛ `rpm -V` فقط وابستگی عمداً ردشدهٔ `ca-certificates` را گزارش کرد. **حدود:** systemd واقعی/شروع سرویس در container اجرا نشد، بسته امضا نشده است، و `brp-strip` به دلیل نبود `strip`/`objdump` رد شد (باینری از قبل `-s -w` است). **پاک‌سازی:** container و دایرکتوری‌های موقت حذف شدند، چیزی روی host نصب نشد و در آن واحد هیچ نوشتنی در repo انجام نشد. **وضعیت:** ادعای «اجرای payload روی amd64» بسته شد؛ arm64/سایر معماری‌ها و نصب واقعی + سرویس تحت RPM همچنان آزمون‌نشده‌اند.

- **رفع مشکل `desktop proxy set` بعد از ویرایش دستی (۲۰۲۶-۱۱-۲۰، ارکستریتور):** کاربر گزارش کرد که پس از apply، تغییر دستی تنظیمات پروکسی دسکتاپ باعث میشود `set` مجدداً با پیام «اول restore کن» رد شود. تصمیم صریح کاربر: `set` دوباره اعمال کند و فقط کلیدهای ویرایششدهٔ کاربر را بهعنوان نقطهٔ restore جدید ثبت کند و بقیهٔ کلیدها Previous اصلیشان را حفظ کنند. پیادهسازی در `internal/platform/desktop.go`: مسیر `Previous.Applied` بهجای رد کردن، مقادیر زنده را میخواند و در صورت تغییر، marker تازه (با `Previous` = مقدار دستی کاربر تنها برای کلیدهای ویرایششده، و Previous اصلی برای بقیه) را قبل از نوشتن ذخیره و سپس مقادیر هدف را مینویسد؛ حالت بدون تغییر no-op میماند و تغییر محیط دسکتاپ یا listenerها همچنان مستلزم restore دستی است. حلقهٔ نوشتن/rollback برای یکسانشدن apply تازه و re-apply به helper استخراج شد. تست ردکنندهٔ قدیمی با چهار تست re-apply جایگزین شد (ثبت نقطهٔ restore، rollback تا مقدار کاربر، رد مقدار غیرقابل restore، لغو هنگام شکست ذخیرهٔ marker). اسناد `docs/contracts.md`، `docs/requirements.md` و README بهروز شدند. **شواهد:** `gofmt` پاک؛ `go vet ./...`، `go build ./...` و `go test -count=1 ./...` سبز (۱۷/۱۷ پکیج، acceptance ۱۱۷s). **اجرا نشد:** جریان زندهٔ گیتشدهٔ دسکتاپ (`ROUTELATCH_DESKTOP_TEST=1`)، چون تغییر تنظیمات دسکتاپ میزبان مجوز جدا میخواهد؛ روی نصب فعلی چیزی اعمال نشد. **بازبینی مستقل:** دور اول `Not ready` با نقص Blocking (جایگزینی `Previous` تمام کلیدها با مقدار فعلی، باعث میشد `restore` کلیدهای دستنخورده را به وضعیت RouteLatch برگرداند) و ۴ nit؛ نقص مادی و nitها در همان مرحله رفع و تست رگرسیون `TestKDERestoreAfterReapplyReturnsEveryKey` اضافه شد. شواهد مجدد: تمام پنج تست جدید روی کد قبلی (`HEAD:desktop.go`) fail و روی کد فعلی pass؛ `go vet`/`go build`/`go test -count=1 ./...` سبز (۱۷/۱۷، acceptance ۱۱۷s). دور دوم بازبینی **Ready** بود (تنها nit غیرمسدودکنندهٔ متن کامنت هم رفع شد). **باقی:** هیچکدام — commit در `88d04b1` و همان کد بهعنوان v0.1.2 در `/usr/bin/routelatch` نصب و سرویس روی آن active است (بازخوانی ۲۰۲۶-۱۱-۲۰).

- **ریلیز `v0.1.2` منتشر شد (۲۰۲۶-۱۱-۲۰، با مجوز صریح کاربر):** تگ annotated `v0.1.2` (شیء تگ `3d1c181a`) روی commit اصلاح پروکسی دسکتاپ `88d04b1` ساخته و همراه با commit به هر دو remote (`github`، `origin`/forgejo) push شد. اجرای سبز CI `44736109624` روی `88d04b1` (هر شش job شامل `rpm-packages`) دروازهٔ انتشار بود. بستهها در `DIST` ایزوله با `VERSION=v0.1.2`، `COMMIT=88d04b1` و `SOURCE_DATE_EPOCH=1700000000` ساخته شدند؛ `check-artifact.sh` و `check-package.sh` pass، دو اجرای packaging همان `SHA256SUMS` بایتیکسان دادند و `sha256sum -c` سبز بود. چهار RPM باینری `routelatch-0.1.2-1.*` با ابزار استخراجشدهٔ rpm 6.0.1 (نصب نشده) ساخته و از نظر متادیتا، فهرست فایلها و نبود scriptlet بازرسی شدند. نخستین تلاش آپلود با `urllib` پیش از ذخیرهٔ هیچ دارایی timeout خورد؛ ریلیز نگه داشته شد و ۱۷ دارایی با `curl` جریانپذیر آپلود شدند. بررسی نهایی API عمومی name/size/digest دقیقاً برابر فایلهای محلی، بدون دارایی اضافی، و تگ annotated روی `88d04b1` را تأیید کرد. جزئیات و checksumها در `docs/release-readiness.md` است.
- **ریلیز `v0.1.3` منتشر شد (۲۰۲۶-۱۱-۲۱، با مجوز صریح کاربر):** تگ annotated `v0.1.3` (شیء تگ `6f226688c8e48ecbc7570383b6e86df259b7b58c`) روی `a32b033` ساخته و همراه با commit به هر دو remote (`github`، `origin`/forgejo) push شد؛ `git ls-remote` هر دو remote همان شیء تگ و peeled commit `a32b033` را نشان می‌دهد. اجرای سبز CI `44883729124` روی `a32b033` (هر شش job شامل `rpm-packages`) دروازهٔ انتشار بود. بسته‌ها از export پاک `git archive a32b033` در `DIST` ایزوله با `VERSION=v0.1.3`، `COMMIT=a32b033` و `SOURCE_DATE_EPOCH=1700000000` ساخته شدند؛ `check-artifact.sh` (amd64) و `check-package.sh` (چهار معماری، یک اجرا) pass، یک اجرای packaging بدون sidecar هم pass، دو اجرای packaging همان `SHA256SUMS` را داد (`sha256 7658f5a0…`) و `sha256sum -c` روی `dist` و `dist/packages` سبز بود. آرشیوهای رسمی sing-box v1.14.4 با digestهای pin‌شده تأیید و محتوایشان بایت‌به‌بایت با باینری عرضه‌شده مقایسه شد. چهار RPM باینری `routelatch-0.1.3-1.*` داخل container یک‌بارمصرف `ubuntu:26.04` با rpm 6.0.1 (بدون نصب روی host) با `rpmbuild --target <arch> -bb` ساخته و از نظر متادیتا، فهرست دقیق فایل‌ها (۱۴ فایل با sidecar روی x86_64/aarch64 و ۱۳ فایل روی riscv64/ppc64le)، نبود scriptlet/trigger و نبود ورودی `/etc/systemd` بازرسی شدند؛ payload `x86_64` با `rpm -i --nodeps` نصب و `version` آن `v0.1.3`/`a32b033` و `sing-box version` برابر 1.14.4/`c005f353` بود. ۱۷ دارایی (چهار tarball، چهار `.deb`، چهار اسپک، چهار RPM و `SHA256SUMS`) با curl جریان‌پذیر آپلود شدند؛ تلاش اول به‌دلیل `Content-Type` پیش‌فرض curl با HTTP 422 رد شد و هیچ دارایی نساخت. بررسی نهایی API عمومی نام/اندازه/digest هر ۱۷ دارایی را دقیقاً برابر فایل‌های محلی، بدون دارایی اضافی، و تگ annotated را روی `a32b033` تأیید کرد. جزئیات و checksumها در `docs/release-readiness.md` است.

- **گروه ج — بخش ۲ انجام شد (۲۰۲۶-۱۱-۲۱، ارکستریتور؛ A04/A06 با اثبات مقصد واقعی، بدون تغییر کد):** طبق تصمیم همان روز مقاصد یک فهرست ثابت GETمحور بودند: `https://github.com/`، `https://www.wikipedia.org/`، `https://example.com/` و `https://www.cloudflare.com/cdn-cgi/trace`، با سقف **حداکثر ۲ درخواست به‌ازای هر exit** و بدون حلقه/ترافیک پایدار. **seed و نمونهٔ ایزوله:** CLI فرمان export ندارد، پس کل pool از **کپی فقط‌خواندنی `state.json` سرویس زنده** (۱۷۶ رکورد، mode 0600، سپس حذف‌شده؛ مسیر `/tmp/opencode/rl-part2/instance`) در نمونهٔ ایزوله با پورت‌های ۱۸۳۱۷/۱۸۳۱۸ بار شد؛ ۳۸ قانون موجود فقط در همان کپی حذف و چهار قانون `domain` دقیق برای همان چهار host اضافه شد (policy ۱۲۳→۱۷۹). یک fixture محلی جداگانه (پورت‌های ۱۸۳۲۷/۱۸۳۲۸ و listenerهای loopback) فقط برای D22 استفاده شد. **بار:** ۱۱ تونل مقصد از pool روی ۶ exit (A–F، هر exit حداکثر ۲) و در مجموع **۲۰ تلاش مقصد** (۱۱ تونل pool، ۷ درخواست مستقیم: ۴ baseline، یک fetch دوم github برای سنجش نوسان، یک CONNECT مستقیم در all-direct برای churn و یک مقصد تأییدنشده، و ۲ تلاش روی pin مرده که به مقصد نرسید)؛ هیچ loop یا ترافیک پیوسته‌ای نبود. **اثبات هر مقصد:** wikipedia از exit B: وضعیت ۲۰۰، ۱۲۲٬۵۰۶ بایت، sha256 **دقیقاً برابر** fetch مستقیم؛ example.com از E و C (و نیز در حالت all-direct): ۲۰۰، ۵۷۷ بایت، sha256 **دقیقاً برابر** مستقیم؛ github از D و F: ۲۰۰ با ۵۳۷٬۲۱۷ و ۵۳۷٬۳۶۹ بایت، اما sha خام **نابرابر** مستقیم است چون صفحهٔ github توکن‌های per-request دارد (`fetch-nonce`، `request-id`، `html-safe-nonce`، `visitor-payload/hmac`، نوار زندهٔ `DataMarquee` و `recentEvents`). دو fetch مستقیم هم با همین کلاس تفاوت نابرابر بودند؛ پس مقایسه **canonical** شد: با mask مستندشده (همان metaها، UUID، `recentEvents`، نوار DataMarquee و پرچم `data-cookie-consent-required`) هر چهار بدنهٔ github (دو مستقیم و دو exit) به sha256 یکسان `802d7a…` و طول ۵۰۲٬۳۲۰ بایت رسیدند — این «برابری canonical» است، نه byte-identity. trace از B و E: ۲۰۰؛ `loc=DE`/`colo=FRA` در برابر `loc=IR`/`colo=LLK` مستقیم، و `ip` خروجی **متفاوت** از IP مستقیم (خود IPها چاپ/نگه‌داری نشدند؛ فقط hash کوتاه و نتیجهٔ مقایسه). **یک تونل در هر تلاش:** هر تلاش pin‌شده بود و pin پنجرهٔ failover را خاموش می‌کند؛ در CONNECT نگه‌داشته‌شده `open=1`, `dialing=0`, `proofs.accepted=1`, `proofs.confirmed=0` (این proof فقط «پذیرش درخواست توسط exit» است، نه تأیید مقصد) و پس از بستن `open=0` دیده شد؛ درخواست‌ها با فاصلهٔ >۱s اجرا شدند تا coalescing یک‌در‌ثانیه‌ای نمونه را پنهان نکند، و افزایش نمونه‌های سلامت با تعداد موفقیت‌ها هم‌خوان بود (B +۲، C +۱، F +۱، A صفر). **محدودیت صریح:** تعداد تماس سمت مقصد روی سرورهای ثالث قابل مشاهده نیست؛ تنها شمارش wire-level همان fixture محلی D22 است و ادعای بالاتر از «یک تونل/یک dial در سمت ما + بدنهٔ منطبق» نمی‌شود. **health (نیمهٔ گمشدهٔ A04/A06):** healthy از ۱ به ۲ و سپس ۳ رسید: B از `unknown` با نمونه‌های ۳۲→۳۴ و EWMA ۲۸۱۴→۱۷۲۵ms به `healthy`؛ E از `unknown` به `healthy` (نمونهٔ نهایی ۴۵، EWMA ۹۸۰ms)؛ D با موفقیت github به `healthy` رسید (شمارش healthy ۱→۲) و بعد با شکست pre-byte در trace به `cooling` و سپس `unknown` برگشت؛ C هم با یک downlink نمونهٔ موفق گرفت (+۱) و سپس با شکست trace سرد شد؛ F (تنها healthy اولیه، ۱۵ نمونه) با یک downlink به ۱۶ رسید و بعد با شکست trace سرد شد؛ A با شکست github بدون هیچ نمونهٔ موفق تازه ماند. مقادیر «قبل» فقط برای A/B/C نگه داشته شد و برای D/E/F گم شد؛ برای آن‌ها شاهد، گذار وضعیت است نه delta. پایان: healthy ۲ (B,E)، cooling مؤثر ۰، usable ۱۵۶، total ۱۷۶، unsupported ۲۰ = ۱۹ سرویس زنده + یک hysteria2 که در نمونهٔ ایزوله چون sidecar نصب نیست unsupported است. rerank/انتخاب خودکار دوباره اجرا نشد؛ این واحد فقط تغییر مشتق health را ثبت کرد. **مقصد تأییدنشده:** `cloudflare.com` (apex؛ فقط `www.cloudflare.com` قانون دارد) ۲۰۰ از مسیر **مستقیم** گرفت: `ip` برابر IP مستقیم، `loc=IR`/`colo=LLK`، و نمونه‌های سلامت A–E بدون تغییر → صفر دخالت exit. **exit مرده/سرد:** یک رکورد fixture از نوع trojan به `127.0.0.1:1` (پورت بستهٔ محلی) اضافه و بعد حذف شد. CONNECT به مقصد تأییدشده از این pin: **۲۰۰ و سپس بستن تونل با صفر بایت مقصد**، بدون fallback — این رفتار مستند `ProofAccepted` برای exitهای core-backed است و **۵۰۲ نیست**؛ همان pin با forwarder سادهٔ HTTP (`http://example.com/`) دقیقاً **`HTTP/1.1 502 Bad Gateway`** با صفر تماس مقصد داد، در حالی که همان مقصد مستقیم ۲۰۰ بود (پس fallback مستقیم رخ نداد). probe این exit را `refused`/`exit_unreachable` و cooling کرد (۲ شکست متوالی)؛ انتخاب دستی همان exit سردشده پذیرفته شد، چون §۲.۴ انتخاب دستی صریح cooling را مجاز می‌داند — پیام خطای `pool select` هنوز cooling را در فهرست «غیرقابل‌استفاده» می‌آورد، اما آن مسیر فقط برای invalid/unsupported/غیرقابل‌dial اجرا می‌شود؛ **ناسازگاری متنی، نه نقص رفتاری**. **churn (نیمهٔ موکول‌شده از بخش ۱):** با یک CONNECT زندهٔ pool به github از F (`open=1`, `proofs.accepted=1`) گذار targeted→all-direct نتیجهٔ `closed={count:1,pending:0}, kept=0, 0ms` و سپس `open=0` داد؛ با یک CONNECT زندهٔ **مستقیم** به example از طریق پروکسی در all-direct (`open=1`) گذار all-direct→targeted هم `closed={count:1,pending:0}, kept=0, 0ms` و `open=0` داد — مطابق جدول §۲.۶. **D22 (اختیاری، فقط fixture محلی):** دو رکورد socks5 محلی به یک origin شمارنده؛ exit اول ۱۳۹ بایت درخواست را forward کرد (origin درخواست ۱ را دید) و پیش از هر بایت پاسخ تونل را بست؛ exit دوم همان درخواست را دید (origin درخواست ۲) و پاسخ را برگرداند؛ کلاینت **فقط یک پاسخ** گرفت و exit اول cooling و دوم healthy شد → **ریسک مستند D22 واقعی است**: در پنجرهٔ pre-first-byte، failover بایت‌های درخواست را در مقصد تکرار می‌کند. این آزمون fixture است، نه pool واقعی؛ replay wire-level روی pool واقعی باز می‌ماند. **حدود:** تنها ۶ exit و ۴ مقصد اثبات شدند، نه همهٔ ۱۵۷ عضو؛ IP خروجی فقط برای B و E با trace اثبات شد و تلاش trace از C/D/F (و github از A) پیش از بایت اول شکست خورد و exit را سرد کرد (علت تعیین نشد؛ رفتار با ثبت مستند `destination_unconfirmed` و cooling هم‌خوان است و شاهدی بر نقص محصول نیست)؛ سه تلاش trace از پنج تلاش شکست خورد و این رفتار مطابق ثبت `destination_unconfirmed` است؛ بدنهٔ github canonical است نه بایتیکال؛ UDP واقعی موضوعیت ندارد چون v1 هیچ پشتیبانی UDP ندارد؛ arm64/GNOME/سایر موارد موکول‌شده دست نخوردند. **ایزولاسیون/secrets:** سرویس زنده پیش و پس `active` با همان `MainPID=2026`، `NRestarts=0`، `ExecMainStatus=0` و listenerهای ۸۳۱۷/۸۳۱۸؛ از این نمونه هیچ فرمان نویسنده‌ای به data-dir زنده فرستاده نشد (فقط کپی فقط‌خواندنی و دو خواندن `config path`/`status`)؛ state زنده در همین بازه توسط خود سرویس rewrite شد، پس hash آن نه شاهد نوشتن ما است و نه رد آن؛ پس از shutdown هیچ listener/process آزمایشی نماند؛ کپی seed، state/log ایزوله، بدنه‌ها و فایل‌های pool/rules حذف شدند و اسکن پیش از حذف روی شاهدهای fetch **۰** hit برای server/display/credential هر رکورد داد؛ شاهد باقی‌مانده فقط aggregate و fetchهای عمومی است (بدون لینک، UUID یا exit id). **HEAD پیش از commit: `1be8e9f`**؛ باینری dev/unstamped از همین HEAD با go1.27.4 و `CGO_ENABLED=0`. **نقص محصول یافت نشد؛ هیچ کد محصول تغییر نکرد.**
- **گروه ج — بخش ۱ انجام شد (۲۰۲۶-۱۱-۲۱، ارکستریتور؛ A06 کامل و A04 در بخش keep/local، بدون تغییر کد):** طبق تصمیم‌های همان روز، pool کاربر یک اشتراک **پرداختی و نیمه‌خصوصی** است (او pool خصوصی ندارد) و استفادهٔ **فقط‌خواندنی** مجاز است؛ سقف بار **یک transport probe به‌ازای هر عضو pool**، **هیچ ترافیک مقصدی** در این واحد، و churn فقط با mode switch/refresh روی نمونهٔ **ایزوله** با data-dir زیر `/tmp/opencode` و دو پورت اختصاصی. مسیر seed: CLI هیچ فرمان export ندارد، پس pool کامل از **کپی فقط‌خواندنی `state.json` سرویس زنده** (۱۷۶ رکورد، mode 0600، سپس حذف‌شده) در نمونهٔ ایزوله بار شد و مسیر import هم جداگانه با کپی فایل منبع زنده اثبات شد. **import تازه:** valid ۳، invalid ۰، unsupported ۱۰ پیش از dedupe و ۱۲ ردیف ذخیره‌شده (یک dedupe از دو رکورد unsupported طبق `dedupe_key`)؛ تفکیک ۹ ردیف unsupported ذخیره‌شده: ۶ `protocol_unsupported` (۵ ss، ۱ vmess) و ۳ `reality_not_verified` (شمارندهٔ import طبق قرارداد پیش از dedupe است و اختلاف ۱۰ با ۹ از همین‌جاست)؛ هیچ ردیفی با protocol/status تهی و هیچ رکورد غیرقابل‌dial به‌عنوان `unknown` گزارش نشد (۳ ردیف پذیرفته‌شده تا پیش از probe همان `unknown` سلامت‌اند). **pool کامل:** ۱۷۶ رکورد (۱۴۹ trojan، ۲۰ vless، ۵ ss، ۱ hysteria2، ۱ vmess) با ۱۹ unsupported: ۱۰ `engine_record_rejected`، ۶ `protocol_unsupported`، ۳ `reality_not_verified`. **`refresh --all`:** هر سه منبع یک pass؛ ۲ منبع چون فایل مبدأشان دیگر وجود ندارد fail شدند و **last-good حفظ شد**، ۱ منبع ok با ۳ رکورد valid؛ total همان ۱۷۶ ماند (قاعدهٔ A06). **transport:** ۱۵۷ عضو usable **دقیقاً ۱۵۷ probe** (۱۳ از دو batch چرخش خودکار + ۱۴۴ دستی، بدون تکرار): ۱۵۶ success و ۱ timeout (۲۰٫۰۸s)؛ توزیع latency موفق‌ها (n=۱۴۳ موفق از pass دستی؛ مدت batchهای چرخش ذخیره نمی‌شود) min ۲۰۳ms / median ۲۳۱ms / p90 ۳۸۴ms / max ۵۴۲۶ms؛ به تفکیک transport (pass دستی): ws ۱۴۱ success، raw ۱ success + ۱ timeout، xhttp ۱ success. **cooldown:** عضو timeout با ۷ شکست متوالی دقیقاً **۹۰۰s** (سقف ۱۵ دقیقه) cooling شد با reason `cooling:timeout`. **health/ranking:** tick ۳۰ ثانیه‌ای با فاصلهٔ چرخش مستند ۱۰ دقیقه مشاهده شد (یک batch ۸ در اولین tick یعنی +۳۰s و یک batch با ۵ شاهد در +~۱۰m۳۰s پس از gate)؛ cooling از ۳ به ۱ و سپس ۰ رسید فقط با normalize‌شدن cooldownهای منقضی؛ پس از pass: healthy ۱، cooling ۱، unknown ۱۵۵، unsupported ۱۹ و usable ۱۵۶؛ `pool rerank` پیش و پس از pass **همان exit سالم** را انتخاب کرد (skip از ۱۵۶ unknown + ۱۹ unsupported به ۱۵۵ unknown + ۱ cooling + ۱۹ unsupported) — **انتخاب خودکار تغییر نکرد** (artifact ترتیب کامل رتبه را ندارد و این مشاهده سطح انتخاب است)، چون transport success عمداً healthy نمی‌سازد و EWMA را به‌روز نمی‌کند (بند ۵.۳/۶.۲ قرارداد). **churn:** روی نمونهٔ pool دو دور targeted→all-direct→targeted (policy ۱۲۳→۱۲۵ و ۱۲۵→۱۲۷) بدون اتصال زنده اجرا شد و پاسخ‌های ثبت‌شده `closed=0, kept=0`اند (پاسخ ۱۲۴ پیش از بازاجرای loopback با همان نام فایل بازنویسی شد و حفظ نشد؛ ۱۲۵/۱۲۶/۱۲۷ محفوظ‌اند)؛ برای سنجش حساب registry، یک **بازاجرای ایزولهٔ بدون pool** با یک CONNECT زندهٔ **loopback** (۲۰۰ و echo، بدون هیچ ترافیک pool/بیرونی) اجرا شد: `open=1` پیش از تغییر، هر دو گذار `kept=1, closed=0` با policy ۱→۳ (artifact محفوظ) و `open=0` پس از بسته‌شدن کلاینت؛ listenerها باز ماندند. **محدودیت:** نیمهٔ closing گذار برای اتصال‌های pool در این واحد اجرا نشد، چون هیچ ترافیک مقصدی مجاز نبود؛ شاهد زندهٔ قبلی (R2/R4/R7) معتبر است و این نیمه در بخش ۲ با مقاصد تثبیت‌شده انجام شد (بالا؛ `kept=0`/`closed=1` در هر دو گذار). **یکپارچگی:** `state.json` معتبر، `schema_version=1`، ۱۷۰٬۸۰۹ بایت، مجموعهٔ کلیدهای top-level و record برابر نسخهٔ زنده، فایل‌ها mode 0600 و data-dir 0700؛ `doctor` سبز با تنها هشدار `quarantined_records` (۱۹). شاهدهای JSON (import، refresh، probe، rerank، mode/churn، doctor) حفظ شده‌اند؛ state و log طبق قاعدهٔ secret حذف شدند، پس اعداد tick، حجم/کلیدهای state، ترکیب pool و تفکیک ۱۹ unsupported، شمار ۱۲/۹ ردیف import، اعداد cooldown (۷ شکست متوالی و ۹۰۰s)، گذار cooling ۳→۱→۰، «بدون protocol/status تهی» و اسکن log **مشاهدهٔ همین اجرا** هستند و artifact برایشان باقی نمانده است. **ایزولاسیون/secrets:** سرویس زنده پیش و پس `active` با همان `MainPID=2026`، `NRestarts=0`، `ExecMainStatus=0` و listenerهای ۸۳۱۷/۸۳۱۸؛ پس از stop هیچ listener/process آزمایشی نماند، خاموشی تمیز بود و `git status` پاک؛ اسکن log ایزوله **۰** hit برای server/display name هر رکورد، **۰** scheme لینک و **۰** UUID داد و کپی state/منبع و data-dir حذف شدند. **HEAD پیش از این commit: `63e4998`** (باینری از همین HEAD با go1.27.4 و `CGO_ENABLED=0` ساخته شد؛ dev/unstamped). **باقی برای بخش ۲:** اثبات CONNECT به مقاصد (فهرست کوچک پایدار GETمحور که هماهنگ‌کننده تعیین می‌کند)، شکست TLS-pin در برابر یک endpoint عمومی، و HTTP CONNECT از خروجی عمومی اگر دقیقاً تعریف شود؛ **UDP واقعی موضوعیت ندارد** چون v1 هیچ پشتیبانی UDP ندارد؛ **wire-level replay** از فهرست گروه ج باز است و در دامنهٔ توافق‌شدهٔ این بخش ۲ نیست (نیازمند تعریف/تصمیم). **نقص محصول یافت نشد.**
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
  با unit موقت انجام شد (ب/۵).** A01 در ۲۰۲۶-۱۱-۲۱ با شهادت کاربر بسته شد (حذف build محلی از این ماشین و نصب `.deb` منتشرشدهٔ `v0.1.2` که طبق انتظار کار کرد)؛ ما محیط پاک را خودمان نیازمودیم.
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
- **R4 انجام شد (`766edcd`، `da333fe`) با اشتراک پرداختی نیمه‌خصوصی کاربر (او pool خصوصی ندارد؛ فقط‌خواندنی، هیچ secret در repo/گزارش):**
  نمونهٔ قطعی ۴ لینک (۲ trojan + ۲ vless، ۴ سرور) از pool با ۱۶۵ خط؛ import → ۳ valid و
  ۱ `unsupported`/`engine_record_rejected` (VLESS عمومی بدون TLS، مستندشده)؛ `refresh --all`
  شکل را حفظ کرد؛ transport ۳/۳ موفق ۲۷۴–۳۹۴ms؛ یک exit با `bytes_down=1` مدرک egress داد و
  دو تای دیگر `destination_unconfirmed` بدون cooling؛ targeted یک درخواست کنترل‌شده →
  CONNECT 200، پاسخ HTTP/2 200 با body بایت‌یکسان، **۱ تماس مقصد**، IP خروجی ≠ IP مستقیم؛
  exit مرده/مسدود → 502 «exit unreachable» با ۰ افزایش شمارنده و بدون fallback (ادعای تاریخی همان آزمون؛ §۲.۱/§۲.۳: مسیر core-backed با CONNECT می‌تواند ۲۰۰ بدهد و پیش از بایت مقصد ببندد — شاهد بخش ۲)؛ all-direct و
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
  exit (۱ تماس، IP خروجی ≠ مستقیم)، مقصد غیرتأییدشده direct، exit مرده → 502 بدون direct (ادعای تاریخی؛ رفتار به proof class و مسیر وابسته است — §۲.۱/§۲.۳ و بخش ۲).
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
| R4 | **انجام شد** (`766edcd`, `da333fe`) با اشتراک پرداختی نیمه‌خصوصی کاربر (او pool خصوصی ندارد؛ فقط‌خواندنی؛ هیچ secret در repo/گزارش): نمونهٔ ۴ لینک از ۱۶۵ خط، import ۳/۳ valid + ۱ `engine_record_rejected` مستند، transport ۳/۳ و health/ranking اندازه‌گیری‌شده، یک درخواست targeted با CONNECT 200 و **۱ تماس مقصد** و IP خروجی ≠ مستقیم، exit مرده → 502 بدون fallback (ادعای تاریخی همان آزمون؛ رفتار به proof class و مسیر وابسته است — §۲.۱/§۲.۳ و بخش ۲)، all-direct/stopped مطابق قرارداد، و grep D21 بدون هیچ hit مقصد. نقص «شواهد downlink واقعی به health نمی‌رسید» رفع شد. **باقی:** مقاصد دلخواه، churn/طولانی‌مدت، و همهٔ اعضای pool | A04/A06 (ردیف عمومی، اثبات محدود) | انجام شد |
| R5 | **انجام شد** (`640f83c`, `5d328a6`): مسیر import هرگز `unknown` نمی‌سازد (http → `unsupported`/`http_exit_not_verified`، scheme ناشناس → `unsupported`/`protocol_unsupported`) و view/status حالا رکورد undialable را **صرف‌نظر از status ذخیره‌شده** جز `invalid` مشتق می‌کند. `pool list`، `status` و `doctor` از یک مالک واحد (`EffectiveStatus`) می‌خوانند، state بازنویسی نمی‌شود و رکورد پیش از هر dial/probe رد می‌شود. **Residual پذیرفته‌شده:** protocol تهی/صفر عمداً دست‌نخورده است؛ semantics پروتکل‌های پشتیبانی‌شده با تنظیمات قابل dial تغییر نکرده (رکورد gecko بدون رمز obfs بعداً زیر همین قاعده مشتق شد — «تصمیم‌های باز»)؛ اولین tick چرخش ۳۰ ثانیه‌ای اکنون در سلول گیت‌شدهٔ long-reuse مشاهده می‌شود ولی چرخهٔ کامل ۱۰ دقیقه‌ای در acceptance درایو نمی‌شود؛ `SyncStatuses` پس از فرمان تغییردهنده ممکن است cooldown منقضی را به `unknown` نرمال کند (bookkeeping قبلی، نه نوشتن مشتق‌سازی) | A06/§5 (کیفیت گزارش) | نه |
| R6 | **انجام شد** (`a69ae1e`): WS/TLS، gRPC/TLS (`h2`) و XHTTP/TLS (`h2`) با exit pin‌شدهٔ محلی، rule، `pool select` و CONNECT به peer loopback واقعاً آزموده شدند (هر سلول دقیقاً ۱ درخواست مقصد و پاسخ بدون تغییر)؛ pin غلط ۰ داده به مقصد می‌فرستد و سرویس بالا می‌ماند. **خارج از v1، نه آزمون‌نشده:** REALITY/ECH، mKCP، HTTPUpgrade و mux/XUDP پشتیبانی نمی‌شوند و h3 به‌دلیل سیاست no-UDP/QUIC کنار گذاشته شده (بخش «تفکیک محدودیت‌ها»). **reuse طولانی‌مدت انجام شد (۲۰۲۶-۱۱-۲۱):** سلول گیت‌شدهٔ `RL_ACCEPT_LONG_REUSE_TEST=1` روی WS/TLS: ۳ درخواست در ۳۲٫۰s با شکاف بی‌کار ۳۰٫۰s، ۱ اتصال مشتری که کل پنجره زنده ماند و هر سه درخواست را حمل کرد، ۱ درخواست مقصد در هر تلاش، بدون تحویل direct (نام fixture روی host resolve نمی‌شود؛ ۱ اتصال مقصد)؛ اولین probe سلامتِ زمان‌بندی‌شده (بررسی tick ۳۰s؛ چرخهٔ بعدی ۱۰ دقیقه) یک اتصال کوتاه‌عمر جدا با ۰ تماس مقصد ساخت | T1/T4 دقت ادعا | نه |
| R7 | **انجام شد — استفادهٔ واقعی و ماندگار:** نصب unit و `enable --now`، pool کاربری import شد، ۳۴ قاعده برای دامنه‌های کاربر، mode `targeted`، پروکسی دسکتاپ KDE روی RouteLatch با مسیر بازگشت. سه بررسی واقعی (approved→exit، unapproved→direct، exit مرده→502) pass — این ردیف تاریخیِ همان آزمون است؛ رفتار CONNECT با exit core-backed به proof class وابسته است (§۲.۱/§۲.۳) و جزئیات بخش ۲ در «وضعیت فعلی» آمده. **شروع در login و reboot (۲۰۲۶-۱۱-۲۰) اثبات شد** | A11/A13 در استفادهٔ واقعی + تجربهٔ dogfood | انجام شد با مجوز شما |
| R8 | **انجام شد (`e098ceb` merge):** README/CI/فایل‌های عمومی، بستهٔ release با checksum/stamp/source offer، اسکن تاریخچه (پاک)، بررسی نام، `docs/release-readiness.md`. | آمادگی انتشار | انجام شد؛ push بعداً با مجوز کاربر انجام شد |
| R10 | **انجام شد (`c1a98f9`, `cbc6a87`, `3425c3f`):** failover CONNECT پیش از ۲۰۰ با سقف ۳ exit و بافر ۶۴ KiB، پین دستی تکـتلاش، بدون direct و بدون تغییر HTTP ساده/SOCKS5؛ ۱۶/۳۰ → ۹۲/۱۰۰ و smoke ۱۹/۲۰؛ نصب و restart سرویس روزمره | A02/A03/D22 | انجام شد |
| R11 | **انجام شد (`654cbb6`، `84efe5e`، `8d4118b`):** پشتیبانی واقعی hysteria2/sing-box با رانتایم جدیگانه sing-box `v1.14.4` کنار Xray — import، فیلدهای قرارداد، handoff محلی SOCKS5، انطباق GPL/بستهبندی، و اثبات ترافیک واقعی (۱ تماس مقصد، fail-closed با ۰ تماس، D21). obfs salamander/gecko با ترافیک واقعی اثبات شد (`internal/acceptance/r11_obfs_test.go`). sidecar بستهٔ منتشرشدهٔ v0.1.2 (amd64) آزموده شد (بخش ب/۴). **باقی (کار باز v1):** session طولانی؛ QUIC roaming در repo قابل بازتولید نیست (نیازمند تغییر مسیر/اینترفیس واقعی) | D07/A06/A08 (گسترش دامنه) | انجام شد با تصمیم معماری + مجوز کاربر |
| R12 | **انجام شد (فاز ۱: `1743044` + `d206130`؛ فاز ۲: `3839d6a` + اصلاح `505a650`):** `.deb` بدون امضا و اسپک RPM (`amd64`→`x86_64`، `arm64`→`aarch64`، `riscv64`، `ppc64le`→Debian `ppc64el`) در چهار معماری، هرکدام با سطر `SHA256SUMS`؛ نسخهٔ واحد (`0.1.0.r12.x`) دقیقاً در فیلد Version هر دو قالب. باینری RPM از همین اسپک‌ها با rpm 6.0.1 ساخته/بازرسی و در `v0.1.1`/`v0.1.2` منتشر شد؛ payload `x86_64` نسخهٔ `0.1.2.8.g42b9e4e` (HEAD `42b9e4e`، پس از تگ v0.1.2) در container یک‌بارمصرف نصب و اجرا شد (ردیف اجرای payload RPM)؛ jobCI `rpm-packages` هم همهٔ چهار RPM را می‌سازد/بازرسی می‌کند. متادیتا/payload/اسپک/checksum توسط `check-package.sh` آزموده و تست منفی gate RPM اجرا شد | A01/A14 (توزیع) | فاز ۱ و ۲ با مجوز کاربر؛ Windows `exe` خارج ازScope |
| نام کوتاه `rlch` | **انجام شد (`e181733`؛ CI `45037281497` سبز):** symlink نسبی `rlch -> routelatch` در tarball/`.deb`/RPM، بدون تغییر نام اصلی یا `ExecStart`؛ gate در `check-package.sh` و CI؛ مستندات D25/§۷.۱/README/بسته‌بندی؛ بازبینی مستقل **Ready with nits** و nitها اعمال شدند؛ باینری RPM در job `rpm-packages` ساخته و بررسی شد | D25/A10/A14 | درخواست کاربر |
| R13 | **R13-A..F انجام شد (۲۰۲۶-۱۱-۲۳/۲۴؛ F: verdict Ready):** D26 برای app/tray، `run --headless`، `autostart` و systemd user service opt-in؛ R13-A tray pin، R13-B runtime، R13-C autostart، R13-D service، R13-E حذف unit/مستندات/migration، R13-F acceptance/review. smoke واقعی KDE app/tray PASS؛ full tests **۲۷۴ PASS/۰ FAIL/۱۵ NOTE** و package checks pass؛ یافته‌های بازبینی نهایی (tray error mapping، marker repair) در `2f1e60d` رفع و بازبینی متمرکز **Ready** شد | A13/A15 | اجرای tray واقعی با data-dir ایزوله انجام شد؛ نصب/rollback واقعی service، reboot و کلیک Quit آزمون‌نشده |
| R14 | **انجام شد (۲۰۲۶-۱۱-۲۵؛ اولین ریلیز ساخته‌شده در CI):** `.github/workflows/release.yml` روی تگ annotated `v*` هر ۱۷ دارایی را در CI می‌سازد/تأیید می‌کند و draft→verify→public منتشر می‌کند؛ dry-runهای سبز `45397329232`/`45400769014`/`45422318159`/`45463989732` و شکست کنترل‌شدهٔ `45399453678` در واحد (a) ثبت‌اند. تلاش‌های ۱ و ۲ در gate annotated شکست خوردند و تلاش سوم draft ساخت ولی verify با endpoint تگ برای draft (404) شکست خورد؛ رفع idمحور/یادداشت‌ها در `d14da10` بازبینی مستقل **Ready with findings** (بدون یافتهٔ مسدودکننده) شد. draft `457460945` با مجوز کاربر حذف و نبود Release/draft با listing احرازشده تأیید شد؛ تگ تازه `v1.0.1` (شیء `4462cc86`) روی `d14da10` با CI سبز `45517940277` و `Release` `45521099138` (job انتشار `138895476159`) منتشر شد: Release عمومی `v1.0.1` (id `457516096`) با ۱۷ دارایی، digests منطبق با `SHA256SUMS` و body برابر پیام تگ. **محدودیت‌های شناخته‌شده:** پنجرهٔ create→id می‌تواند draft یتیم بگذارد؛ `check` صفحه‌بندی را با `per_page=100` فرض می‌کند؛ مسیر fail/cleanup فقط stub شده؛ `workflow_dispatch` اجرا نشده؛ flakiness گاه‌به‌گاه step `Test` مشاهده شد. | A14 (انتشار) | مجوز صریح ۲۰۲۶-۱۱-۲۵ |
| R15 | **R15-0 تا R15-H انجام شدند؛ v1.1.0 منتشر شد (۲۰۲۶-۱۱-۲۶):** سطح کنترل tray (D28/A16) با منوی کامل runtime، confirmation دو مرحله‌ای، job/refresh، `restart-engine` سطح process، `Terminal menu` و صفحه‌بندی pool/rules؛ R15-A شاهد منو/زیرمنو/تغییر زنده روی Plasma 6.6.7 را ثبت کرد (شکاف: Wayland/GNOME و دو اسکرین‌شات)، R15-B0 فیلد `resume_requires_restart` را با تست/بازبینی Ready اضافه کرد، R15-B بستهٔ model/controller `internal/trayui` را با تست‌های fake caller/control server و race پیاده کرد و R15-C restart سطح process را با generation/token و بازبینی lifecycle Ready گذراند و R15-D1 binder/runner با backend ساختگی و تست‌های idempotent/race/leak را با بازبینی Ready پیاده کرد و R15-D2 اتصال واقعی deedles/lifecycle/notifier و اجرای KDE gated را با بازبینی Ready گذراند و R15-E منوی pool را audit و دو انحراف ترتیب label/سقف را رفع کرد و R15-F rules/proposals را audit و یک قاعدهٔ availability را یکسان کرد و R15-G1 سطح desktop/diagnostics/data/settings را با چهار اصلاح تکمیل کرد و R15-G2 Terminal menu/Quit را واقعی و audit کرد و R15-H پذیرش/مستندات/بازبینی نهایی را با یک رفع race و شاهد D-Bus/D21 گذراند. هیچ تنظیم میزبان/سرویس تغییر نکرد. **انتشار:** تگ annotated `7abe9704…` روی `532c169` (هر دو remote)؛ CI سبز `45796357850`؛ `Release` `45800238447`؛ Release عمومی id `459257636` با ۱۷ دارایی. | A16 | مجوز ایستادهٔ ۲۰۲۶-۱۱-۲۶ |
| انتشار | **انجام شد (`3cb7bd7` هنگام push، HEAD پیش از R12 فاز ۱ `a028469`):** بازنویسی تاریخچه در کپی کاری، push به forgejo و GitHub، تأیید عمومی بودن GitHub و برابری remote heads | A14 (انتشار) | مجوز کاربر |
| انتشار `v0.1.1` | **انجام شد (تگ روی `2f63814`):** ۱۷ دارایی شامل چهار tarball، چهار `.deb`، چهار اسپک، چهار RPM با نسخهٔ `0.1.1` و `SHA256SUMS`؛ آپلود اول ناقص بود و با حذف پنج دارایی اشتباه و جایگزینی آنها اصلاح شد؛ تأیید نهایی API عمومی نام/اندازه/digest همگی منطبق | A14 (انتشار) | مجوز صریح کاربر |
| انتشار `v0.1.2` | **انجام شد (تگ annotated روی `88d04b1`):** ۱۷ دارایی شامل چهار tarball، چهار `.deb`، چهار اسپک، چهار RPM باینری `0.1.2` و `SHA256SUMS`؛ CI سبز `44736109624`؛ آپلود نخستین تلاش timeout خورد ولی هیچ دارایی ذخیره نشد و با curl جریانپذیر کامل شد؛ تأیید نهایی API عمومی name/size/digest منطبق | A14 (انتشار) | مجوز صریح کاربر |
| انتشار `v0.1.3` | **انجام شد (تگ annotated `6f226688` روی `a32b033`):** ۱۷ دارایی شامل چهار tarball، چهار `.deb`، چهار اسپک، چهار RPM باینری `0.1.3` و `SHA256SUMS`؛ CI سبز `44883729124` (هر شش job)؛ دو اجرای packaging همان `SHA256SUMS` (`7658f5a0…`)؛ RPMها در container یک‌بارمصرف `ubuntu:26.04` با rpm 6.0.1 ساخته/بازرسی و payload `x86_64` اجرا شد؛ تأیید نهایی API عمومی name/size/digest منطبق، بدون دارایی اضافی، تگ annotated روی `a32b033` | A14 (انتشار) | مجوز صریح کاربر |
| انتشار `v1.0.1` | **انجام شد — اولین ریلیز ساخته‌شده در CI (۲۰۲۶-۱۱-۲۵):** تگ annotated `4462cc86` روی `d14da10` (هر دو remote)؛ CI سبز `45517940277`؛ `Release` `45521099138` (job انتشار `138895476159`)؛ Release عمومی id `457516096` با ۱۷ دارایی نسخه‌درست، digests منطبق با `SHA256SUMS` (sha256 خود فایل `3f683f70…dab7d8`) و body برابر پیام تگ؛ بدون ساخت/آپلود محلی | A14 (انتشار) | مجوز صریح کاربر ۲۰۲۶-۱۱-۲۵ |
| انتشار `v1.1.0` | **انجام شد — سطح کنترل tray (۲۰۲۶-۱۱-۲۶):** تگ annotated `7abe9704f66cbcc78fe6340093e8a365324b9407` روی `532c1697da6140fdfd5258dc98da2c1f7927cc61` (هر دو remote)؛ CI سبز `45796357850`؛ `Release` `45800238447` (plan/build/package/RPM/assemble/publish)؛ Release عمومی id `459257636` با ۱۷ دارایی و body آغازشده با «RouteLatch v1.1.0 — tray control surface»؛ بدون draft/prerelease | A14/A16 (انتشار) | مجوز ایستادهٔ ۲۰۲۶-۱۱-۲۶ |

**قاعدهٔ انتخاب:** R1–R8 و R10–R14 و انتشار بسته شدند؛ R9 (انتخاب کاربر) وجود ندارد؛ **`v1.0.0`، `v1.0.1` و `v1.1.0` منتشر شدند و R14 (اولین ریلیز ساخته‌شده در CI) کامل شد. هیچ کار باز v1 نمانده است.** R15 یک capability پس از v1 است که در ۲۰۲۶-۱۱-۲۵ تعریف شد؛ R15-0 تا R15-H انجام و `v1.1.0` در ۲۰۲۶-۱۱-۲۶ منتشر شد؛ هیچ کار باز R15 نمانده است.
**R12 کاملاً بسته شد:** `.deb` بدون امضا + checksum و اسپک RPM چهار معماری، با نسخهٔ واحد و CIjob ساخت/بازرسی RPM. **هیچ فاز بازی از R12 نمانده است**؛ Windows `exe` تا implementation-ready شدن milestone ویندوز وارد نمیشود. تگ `v0.1.0` / ریلیز GitHub دستنخورده است.

کارهای باز v1 و محدودیت‌های محیط/مجوز در بخش «تفکیک محدودیت‌ها» فهرست شده‌اند؛
گروه ج در ۲۰۲۶-۱۱-۲۱ به‌عنوان batch بعدی مجاز شد (**هر سه بخش انجام شد**؛ محدودیت‌ها، اعداد و نمونهٔ ایزوله همان‌جا؛ wire-level replay روی pool واقعی در ۲۰۲۶-۱۱-۲۲ اثبات شد — یک مقصد/دو exit، بدون تعمیم) و سایر اقدام‌های میزبان همچنان مجوز جدا می‌خواهند (corrupt-state
آفلاین در ۲۰۲۶-۱۱-۲۱ انجام شد و مجوزش مصرف شد). push، تگ و ریلیزهای
`v0.1.1`، `v0.1.2` و `v0.1.3` با مجوز صریح کاربر انجام شدند؛ انتشار `v0.1.3` برای
همین batch در ۲۰۲۶-۱۱-۲۱ مجاز شد و در همان تاریخ منتشر شد (قاعدهٔ تحویل در
`AGENTS.md`). batch شواهد گروه ج (`1be8e9f`، `79c0e53`) فقط push شد؛ ریلیز `v0.1.4`
که برایش ساخته شده بود به درخواست کاربر پس‌گرفته شد و آخرین ریلیز در آن تاریخ `v0.1.3` بود.

**شروع R14 واحد (c) — اولین انتشار CI (۲۰۲۶-۱۱-۲۵):** کاربر انتشار **`v1.0.1`**
(patch) را به‌عنوان اولین ریلیز ساخته‌شده در CI مجاز کرد. ترتیب اجرا: یک commit
مستندات روی `main` بدون `[skip ci]` (همین commit شروع؛ بدون ادعای تکمیل)، سبز شدن
`CI` روی همان SHA، سپس تگ annotated `v1.0.1` روی همان commit و push آن برای اجرای
`Release`. تا پایان موفق run انتشار، تکمیل اعلام نمی‌شود.

**نتیجهٔ تلاش اول (۲۰۲۶-۱۱-۲۵) — متوقف:** commit شروع `fb969fb` push شد و `CI`
`45490941645` روی همان SHA سبز شد (هر شش job). تگ annotated `v1.0.1` (شیء
`da6307e`) روی `fb969fb` ساخته و به هر دو remote push شد؛ اجرای `Release`
`45492100825` (job `138792821952`) در gate شکست خورد و jobهای
package/RPM/assemble/publish همه skip شدند؛ هیچ Release یا draft ساخته نشد و
فهرست عمومی releases در آن زمان هنوز به `v1.0.0` ختم می‌شد.

**نتیجهٔ تلاش دوم (۲۰۲۶-۱۱-۲۵) — متوقف:** پس از تغییر اول gate (`4f282a4`)،
commit `5ebbf57` push شد و retry مربوط به step ناپایدار `Test` ثبت شد؛ تگ
`v1.0.1` بازسازی شد (شیء `3b9a8620`) روی `5ebbf57` و push شد. اجرای `Release`
`45502745805` در همان job (`138829983146`) با log احرازشدهٔ
`release gate FAILED: tag v1.0.1 is not annotated` و `SHA: 5ebbf57...` شکست
خورد؛ بدون Release یا draft.

**علت واقعی (هر دو تلاش):** `actions/checkout@v4` با `fetch-depth: 0` ابتدا همهٔ
branchها/tagها را fetch می‌کند، سپس چون `git rev-parse refs/tags/v1.0.1` برای تگ
annotated شیء تگ را برمی‌گرداند (نه `github.sha` که commit است)، با refspec
هدفمند `+<commit>:refs/tags/v1.0.1` دوباره fetch می‌کند. Git این را به‌صورت ref
lightweight روی commit ذخیره می‌کند؛ خط `[tag update] ... -> v1.0.1` در log همان
به‌روزرسانی اجباری است. بنابراین `git cat-file -t refs/tags/v1.0.1` مقدار `commit`
داد و gate annotated شکست خورد، هرچند تگ pushشده annotated بود. منبع checkout:
`getRefSpec` برای `refs/tags/*` با commit مقدار `+${commit}:${ref}` می‌دهد
(actions/checkout v4.2.2، `src/ref-helper.ts`). مسیر branch در dry-run این gate را
اجرا نمی‌کند.

**تشخیص قبلی غلط بود:** ادعای «`github.sha` شیء تگ است و مقایسهٔ خط ۷۸ شکست
خورد» از annotation عمومی check-run که فقط نام step را نشان می‌داد برداشت شده
بود. log احرازشدهٔ تلاش دوم خطای واقعی را صریح می‌گوید و تلاش اول هم همان خطای
annotated را داشت. تغییر `4f282a4` (peel یک‌بارهٔ `$SHA`) بی‌ضرر است و به‌عنوان
guard دفاعی می‌ماند، ولی علت را رفع نمی‌کرد: در push تگ `$SHA` همان commit است.
هارنس محلی قبلی که با قرار دادن شیء تگ در `$SHA` خطا را «بازتولید» می‌کرد،
شکلی را می‌آزمود که GitHub تولید نمی‌کند.

**رفع gate (۲۰۲۶-۱۱-۲۵):** در شاخهٔ tag، workflow پیش از هر بررسی تگ، تگ واقعی را
restore می‌کند
(`git fetch --force --quiet origin "+refs/tags/$tag:refs/tags/$tag"`) و سپس
`git cat-file -t refs/tags/$tag` باید `tag` باشد (پیام شکست نوع واقعی را نام
می‌برد)؛ پیام با `git for-each-ref --format='%(contents)'` از همان ref خوانده
می‌شود، شیء به commit peel می‌شود و با `$commit` (که از peel دفاعی `$SHA` آمده)
مقایسه می‌شود. `$commit` همچنان در on-main، پرس‌وجوی `head_sha=` CI، خروجی
`commit` و `COMMIT` استفاده می‌شود. peel `$SHA`، الگوی `v*`، الزام annotated و
پیام غیرخالی، تطابق تگ↔commit، on-main، CI سبز، رفتار dry-run و
`workflow_dispatch` و permissionها تضعیف نشدند؛ fetch ناموفق، ref غایب یا ref
غیر-tag پیام fail-closed جدا دارند. هیچ step ساخت/انتشار تغییر نکرد و ساختار
workflow بازچینی نشد.

**آزمون محلی (۲۰۲۶-۱۱-۲۵، `/tmp/opencode/rl-gate2`):** بلوک `run:` با PyYAML
استخراج و روی یک remote bare و cloneی که `refs/tags/v1.0.1` در آن lightweight است
(شکل checkout) اجرا شد؛ `curl` stub و URL آن ثبت شد. ۱۴/۱۴ سناریو مطابق انتظار:
تگ annotated با ref محلی lightweight pass شد، ref را به شیء تگ برگرداند و
`version=v1.0.1`/`publish=true`/`tag=v1.0.1`/commit peelشده را داد و CI را با
`head_sha=<commit>` پرسید؛ همان سناریو با gate قبلی با پیام
`tag v1.0.1 is not annotated` شکست خورد (بازتولید همان پیام گزارش‌شده) و همان
setup با ref محلی annotated از gate قبلی pass شد (جداسازی علت)؛ شبیه‌سازی مستقیم
checkout (fetch همهٔ تاریخچه، سپس
`git fetch --no-tags origin +<commit>:refs/tags/v1.0.1`، سپس force-fetch gate)
نوع ref را `tag` → `commit` → `tag` با commit درست نشان داد. رد شدند: تگ lightweight
روی remote، پیام خالی، پیام فقط‌فاصله، تگ روی commit دیگر (پیام شیء تگ و هر دو
commit را نام می‌برد)، commit خارج از main، و نبود CI سبز. تگ غایب روی remote با
`could not fetch tag v1.0.1 from origin` رد شد و به‌عنوان annotated pass نشد؛ نبود
ref محلی با fetch صریح بازسازی و pass شد؛ `$SHA` برابر شیء تگ هم به commit peel
شد؛ تگ غیر-`v*` رد شد؛ dry-run با `v0.0.0-dryrun`/`publish=false` و بدون
پرس‌وجوی CI ماند؛ `workflow_dispatch` روی branch با `publish=true` رد شد و روی تگ
با `publish=false` فقط gateها را اجرا کرد. `bash -n` بلوک و `yaml.safe_load`
workflow pass. این شاهد محلیِ همان دور است؛ در آن زمان هیچ push تگی gate جدید را
اجرا نکرده بود، job انتشار هنوز اجرا نشده بود و `v1.0.1` منتشر نشده بود (آخرین
Release عمومی همان `v1.0.0` بود). تلاش سوم (پایین) بعداً gate را pass کرد و تا
draft/آپلود رسید.

**CI و retry (۲۰۲۶-۱۱-۲۵):** اجرای `45494443321` روی `4f282a4` پنج job از شش را
سبز کرد ولی step «Test» شکست خورد؛ لاگ step بدون احراز هویت در دسترس نیست. diff از
آخرین commit سبز (`fb969fb`) هیچ فایل Go ندارد (فقط workflow و دو سند)، پس شکست به
این تغییر نسبت داده نمی‌شود. مجموعهٔ کامل تست محلی با Go پین‌شدهٔ 1.27.4 pass شد
(`CGO_ENABLED=0 GOPROXY=off go test -count=1 ./...`: هر ۲۲ پکیج،
`internal/acceptance` در ۱۱۸ ثانیه). `0131688` (`45496529871`) هم همان step را
شکست داد و `5ebbf57` retry بعدی را ثبت کرد؛ شکست تلاش دوم پیش از gate CI رخ داد،
پس آن gate ارزیابی نشد. دروازهٔ انتشار همچنان به اجرای سبز روی commit نهایی تگ
نیاز دارد.

**تلاش سوم و دور رفع (۲۰۲۶-۱۱-۲۵):** commit `4121481` push شد و CI آن سبز بود؛
تگ `v1.0.1` (شیء `cce34dcb02cf354f2574d42a02c10bb2d9020b85`) روی همان commit
بازسازی و push شد. اجرای `Release` `45510002182`: jobهای plan/package/RPM/assemble
سبز شدند و job انتشار `138857993885` draft را ساخت و ۱۷ دارایی را آپلود کرد، ولی
در step «Verify name, size and digest of every uploaded asset» شکست خورد؛ هیچ
Release عمومی ساخته نشد.

**علت اول (draft در endpoint تگ دیده نمی‌شود):** `GET
/repos/{repo}/releases/tags/{tag}` برای draft مقدار 404 می‌دهد. verify از همین
endpoint استفاده می‌کرد و در اولین درخواست شکست خورد — بسیار پیش از بودجهٔ
۱۲×۵ ثانیه — هرچند draft و ۱۷ دارایی وجود داشتند. cleanup هم همان lookup را
تکرار کرد، 404 دید و چیزی حذف نکرد؛ بنابراین draft (id `457460945`) باقی ماند و
با بررسی بدون احراز هویت دیده نمی‌شد. این ادعای قبلی که «هیچ draft وجود ندارد»
نادرست بود. draft شناسایی‌شده با مجوز صریح کاربر حذف شد و listing احرازشده نبود
Release یا draftی را تأیید کرد؛ این تغییر خودش آن را نمی‌دید و حذف نمی‌کرد.

**علت دوم (یادداشت‌ها از commit):** step انتشار یادداشت‌ها را با
`git tag -l --format='%(contents)' "$TAG"` از همان checkout می‌خواند؛
`actions/checkout` ref تگ را lightweight روی commit می‌گذارد، پس `%(contents)`
پیام commit را برگرداند و body draft پیام commit شد، نه پیام تگ annotated
(روی draft باقی‌مانده مشاهده شد). gate پلن پیام درست را اعتبارسنجی کرده بود، ولی
publish دوباره از ref اشتباه خواند.

**رفع:** (۱) create پس از ساخت draft، با
`gh api --paginate --slurp "repos/$REPO/releases?per_page=100"` (listing
احرازشده که draftها را شامل می‌شود) دقیقاً یک draft با همان `tag_name` را پیدا
می‌کند، `id` را output می‌دهد و فقط بعد از آن `created=true` می‌نویسد؛ نبود id
fail-closed است. (۲) `scripts/release-verify-upload.sh` مقدار `RELEASE_ID` را
می‌گیرد، `GET /releases/{id}` را polling می‌کند و علاوه بر ۱۷ دارایی/نام/اندازه/
digest، `.tag_name == TAG` و `.draft == true` را الزام می‌کند؛ 404/5xx و خطای
transport داخل بودجه retry می‌شوند و هر پاسخ دیگر fail-closed است؛ هیچ lookup
تگی باقی نمانده. (۳) `release-guard.sh check` همهٔ صفحه‌های listing را می‌خواند و
وجود هر Release با همان tag (draft یا عمومی) را با id و وضعیت رد می‌کند؛ خطای
API، صفحهٔ غیر-array یا JSON ناخوانا fail-closed است. (۴) `delete-draft` فقط
`RELEASE_ID` را می‌پذیرد، دوباره با id می‌خواند، `.draft == true` و
`.tag_name == TAG` را الزام می‌کند و سپس با `DELETE /releases/{id}` حذف می‌کند؛
Release عمومی، tag دیگر، 404 یا خطای API دست‌نخورده می‌ماند؛ حذف با نام tag حذف
شد. (۵) step یادداشت‌ها `+refs/tags/$TAG:refs/tags/$TAG` را force-fetch می‌کند،
نوع `tag` را الزام می‌کند، `%(contents)` را از همان شیء می‌خواند، بررسی غیرخالی
بودن را نگه می‌دارد و شیء تگ را log می‌کند. trigger، permission، gate پلن و
gate انتشار تغییر نکردند.

**آزمون محلی (۲۰۲۶-۱۱-۲۵، `/tmp/opencode/rl-draft-id`):** `curl`/`gh` stub
رفتار واقعی را شبیه‌سازی می‌کند (tag endpoint برای draft = 404؛ listing شامل
draft و صفحه‌بندی؛ حذف فقط با id). ۳۱/۳۱ بررسی pass شد: verify با ۱۷ دارایی
منطبق pass و با دارایی گم‌شده/اضافی/digest نادرست/digest تهی پس از polling/tag
نادرست/Release عمومی/5xx پایدار fail؛ یک 404 گذرا retry و سپس pass؛ `check`
draft یافت‌شده از listing و Release عمومی را رد می‌کند، نبود Release را pass
می‌کند، draft را در صفحهٔ دوم پیدا می‌کند و روی 500 fail-closed است؛
`delete-draft` فقط id داده‌شده را حذف می‌کند و Release عمومی، id ناشناس، 500 و
tag دیگر را دست‌نخورده می‌گذارد؛ کد قدیمی همان حادثه را بازتولید می‌کند (verify
قدیمی «cannot read the draft release»، check قدیمی «no release exists» و cleanup
قدیمی «nothing to delete»)؛ create step مقدار `id=4242`/`created=true` می‌دهد و
بدون id fail-closed است؛ روی یک remote محلی و clone با ref lightweight (شکل
checkout) step قدیمی پیام commit و step جدید پیام تگ annotated را می‌نویسد، و
پیام تگ خالی و تگ lightweight رد می‌شوند. `sh -n` روی هر دو script، `bash -n` روی
stepهای استخراج‌شده و `yaml.safe_load` روی workflow pass شدند. این شواهد محلی و
stubشده‌اند؛ رفتار گذرای digest، صفحه‌بندی واقعی و حذف واقعی draft آزموده نشده‌اند.

**قدم بعد (اکنون انجام‌شده):** listing احرازشده نبود هیچ draft یا Releaseای برای
`v1.0.1` را تأیید کرد (draft `457460945` با مجوز کاربر حذف شد)؛ این رفع merge شد،
CI روی `d14da10` سبز شد، تگ annotated روی همان commit بازسازی و `Release` اجرا
شد. نتیجهٔ نهایی در بخش R14 «وضعیت فعلی» ثبت است.

### آنچه پذیرفته/اثبات نشده است

| معیار | وضعیت | دلیل/حد شاهد |
| --- | --- | --- |
| A02، A03، A05، A06، A08، A12 | اثبات محلی + گروه ج بخش ۱–۳ برای A06 | شمارندهٔ تماس مقصد/خروجی و assertion مستقیم در harness؛ A06 شامل مشتق‌سازی legacy و مسیر import هر دو قاعده؛ گروه ج بخش ۱ (۲۰۲۶-۱۱-۲۱) import کامل pool (۱۷۶ رکورد، ۱۹ unsupported با تفکیک دلیل) و `refresh --all` با حفظ last-good در دو منبع ازکارافتاده را روی نمونهٔ ایزوله اثبات کرد؛ بخش ۲ (۲۰۲۶-۱۱-۲۱) چهار مقصد GETمحور را روی ۶ exit با شمارش تونل، مقایسهٔ بدنه و health مشتق‌شده اثبات کرد (حدود در همان ردیف)؛ بخش ۳ (۲۰۲۶-۱۱-۲۱) روی یک عضو TLS واقعی: pin غلط ۰ درخواست در سمت مقصد و `cooling:exit_unreachable` داد و کنترل با pin حذف‌شده ۱ درخواست/۲۰۰ داد (۱ عضو، بدون تعمیم) |
| WS/gRPC/XHTTP روی TLS | اثبات محلی (R6) | peer loopback pin‌شده، ۱ درخواست مقصد، pin غلط با ۰ داده؛ h3 خارج از v1 است (سیاست no-UDP/QUIC)؛ reuse طولانی‌مدت WS/TLS در سلول گیت‌شدهٔ `RL_ACCEPT_LONG_REUSE_TEST=1` آزموده شد (۳ درخواست/۳۲٫۰s، شکاف ۳۰٫۰s، ۱ اتصال مشتری زنده در کل پنجره) |
| A04، A07، A09، A10 | اثبات محلی + نمونهٔ عمومی محدود (R4) + گروه ج بخش ۱–۳ (۲۰۲۶-۱۱-۲۱) | mode switch زنده+در جریان، rerank/cooldown، مجوز فایل/socket، CLI، launcher env، crash/restart، و ۴ exit عمومی با health/ranking واقعی؛ **گروه ج بخش ۱:** کل pool (۱۷۶ رکورد)، ۱۵۷ transport probe بدون تکرار، cooldown سقف ۹۰۰s، یک batch در اولین tick و یک batch پس از gate ۱۰ دقیقه‌ای، انتخاب خودکار بدون تغییر (ترنسپورت healthy نمی‌سازد)، mode switch با listener/state سالم و شاهد `kept=1/closed=0` برای یک اتصال loopback؛ گروه ج بخش ۲ (۲۰۲۶-۱۱-۲۱) گذار targeted↔all-direct را با CONNECT زندهٔ pool و مستقیم سنجید: هر دو `closed count=1 pending=0 kept=0` و `open 1→0`، بدون fallback؛ فقط ۶ exit/۴ مقصد |
| A13 | **مدل D26 پیاده شد (R13-B/C/D)؛ smoke واقعی app/tray روی KDE PASS؛ نصب/rollback واقعی service و reboot آزمون‌نشده** | smoke واقعی با data-dir ایزوله: `run --json` پس از readiness برگشت، `status.ready`/`listeners_open`، tray روی watcher ثبت شد، SIGHUP/بستن pty ادامه داد، SIGTERM/SIGKILL supervisor بدون orphan و با بستن listener؛ گیت‌شدهٔ autostart واقعی PASS. **مدل قبلی (R1 + بررسی ۲۰۲۶-۱۱-۲۰ + corrupt-state ۲۰۲۶-۱۱-۲۱)** شاهد تاریخی است؛ corrupt-state حالا آفلاین با `run --headless` پوشش داده می‌شود. **باقی:** نصب/rollback/linger واقعی user manager و upgrade unit قدیمی `/usr/lib` آزمون‌نشده؛ فقط host/amd64 |
| A01 | **پذیرفته و بسته (شهادت کاربر، ۲۰۲۶-۱۱-۲۱)** | معیار با شهادت کاربر برآورده شد، نه با اجرای ما: کاربر build محلی را از این ماشین برداشت و `.deb` منتشرشدهٔ `v0.1.2` را روی آن نصب کرد و طبق انتظار کار کرد. ما محیط پاک را نیازمودیم و ادعای آزمون آن را نمی‌کنیم؛ شاهدهای محلی همچنان مسیر نصب واقعی/launch (R1)، metadata/payload `.deb`، اسپک RPM و اجرای payload `0.1.2.8.g42b9e4e`/amd64 در container یک‌بارمصرف‌اند. آزمون آیندهٔ VPS به تصمیم کاربر است و کار باز پذیرش شمرده نمی‌شود |
| A11 | **اثبات واقعی روی KDE (R2)** | set/restore با diff OS، مالکیت، ویرایش کاربر، crash recovery؛ **GNOME به‌تعویق‌افتاد (اولویت پایین، تصمیم کاربر ۲۰۲۶-۱۱-۲۱):** نشست واقعی GNOME آزموده نشده و این یک محدودیت شناخته‌شده است، نه کار معلق این چرخه |
| A14 | **مجوز/ممیزی/بسته (R3+R8+R12)** | Apache-2.0، ممیزی ۶۸ ماژول (چهار معماری cross-build)، tarball/`.deb`/اسپک RPM با `SHA256SUMS` و نسخهٔ واحد؛ R12 `.deb` و اسپکها را محلی تأیید کرد؛ jobCI ساخت/بازرسی RPM اضافه شد (اجرای سبز `44682457124`)؛ ریلیزهای `v0.1.1`، `v0.1.2`، `v0.1.3`، `v1.0.0`، `v1.0.1` و `v1.1.0` هر کدام با ۱۷ دارایی شامل RPM باینری منتشر و تأیید شدند؛ `v1.0.1` و `v1.1.0` در CI ساخته و منتشر شدند |
| A15 | **پیاده‌سازی شد (R13-A..E)؛ smoke واقعی app/tray و package checks pass؛ نصب واقعی service/autostart login و کلیک Quit در tray آزمون‌نشده** | `run` پیش‌فرض detached/tray/readiness، `--headless` foreground، autostart و service idempotent، حذف unit/autostart/postinst از بسته، fail صریح نبود tray/pidfd؛ fake-tray/child و package checks برای idempotency/rollback؛ smoke واقعی KDE (readiness، tray registration، SIGHUP، SIGTERM/SIGKILL بدون orphan) و گیت‌شدهٔ autostart PASS. **باقی:** نصب/rollback/linger واقعی و Quit با کلیک منو؛ GNOME/SSH واقعی؛ pidfd ENOSYS/EPERM؛ runtime غیر-amd64 |
| `.deb` توزیع (R12 فاز ۱) | **ساخت/اعتبارسنجی محلی** | چهار معماری (`amd64`، `arm64`، `riscv64`، `ppc64el`)؛ `check-package.sh` مجموعهٔ دقیق `SHA256SUMS`، control بدون maintainer script، مجموعهٔ دقیق payload، mode/owner و اعتبار نام معماری Debian را میسنجد؛ تکرار بایتیکسان؛ تستهای منفی حذف خط checksum/postinst/فایل اضافه/mode غلط همه fail شدند؛ `arm64` runtime-untested و `riscv64`/`ppc64le` فقط cross-build |
| اسپک/باینری RPM (R12 فاز ۲) | **اسپک: ساخت/اعتبارسنجی محلی؛ باینری: ساخت/بازرسی و اجرای payload روی amd64** | چهار اسپک (`x86_64`، `aarch64`، `riscv64`، `ppc64le`) با Version برابر `.deb`، منبع pin‌شده با SHA256، `%files` شامل LICENSE/NOTICE در `/usr/share/licenses/routelatch/` و بقیه اسناد در `/usr/share/doc/routelatch/`، بدون scriptlet/فعال‌سازی سرویس؛ تست منفی ۷ حالت fail کرد؛ rpmbuild/rpm/rpmspec به‌صورت سیستمی نصب نشدند؛ rpm 6.0.1 استخراج‌شده باینری‌های `0.1.1` را ساخت/بازرسی کرد؛ payload `x86_64` نسخهٔ `0.1.2.8.g42b9e4e` (HEAD `42b9e4e`، پس از تگ v0.1.2) در container یک‌بارمصرف نصب و اجرا شد؛ jobCI `rpm-packages` در اجرای `44682457124` سبز شد |
| hysteria2/sing-box (R11) | **اثبات محلی با ترافیک واقعی** | هارنس گیت‌شدهٔ `RL_ACCEPT_HYSTERIA2_TEST=1`: handshake TLS واقعی با باینری pinشده، echo بایتیک‌سان، ۱ تماس مقصد، fail-closed ۰ تماس، D21؛ فقط loopback. obfs salamander/gecko با سلول گیت‌شدهٔ `RL_ACCEPT_HYSTERIA2_OBFS_TEST=1` اثبات شد؛ **باز v1:** session طولانی؛ sidecar بستهٔ منتشرشدهٔ v0.1.2/amd64 آزموده شد (بخش ب/۴)؛ QUIC roaming در repo بازتولیدپذیر نیست؛ مقصد/exit عمومی در گروه ج است |
| wire-level replay، UDP واقعی، مقاصد دلخواه، churn و خروجی‌های عمومی واقعی (گروه ج) | **بخش ۱–۳ انجام شدند (۲۰۲۶-۱۱-۲۱)؛ D22 روی pool واقعی در ۲۰۲۶-۱۱-۲۲ اثبات شد (یک مقصد/دو exit، بدون تعمیم)** | scope در «تفکیک محدودیت‌ها/ج»؛ اشتراک پرداختی نیمه‌خصوصی کاربر (او pool خصوصی ندارد)، فقط‌خواندنی، بدون لینک/credential/دادهٔ اتصال خصوصی در repo/fixture/لاگ/گزارش و بدون بار سنگین روی نودهای مشترک؛ روی نمونهٔ ایزوله با data-dir و portهای اختصاصی؛ **بخش ۱:** ۱۷۶ رکورد، ۱۵۷ probe بدون تکرار (۱۵۶ success/۱ timeout، median ۲۳۱ms روی n=۱۴۳)، cooldown سقف ۹۰۰s، refresh با حفظ last-good و churn mode switch (شاهد `kept=1` در بازاجرای loopback)، بدون هیچ ترافیک مقصد؛ **بخش ۲:** ۴ مقصد GETمحور روی ۶ exit (حداکثر ۲ درخواست هر exit)، health از unknown→healthy با downlink واقعی، مقصد تأییدنشده direct، exit مرده: CONNECT 200 + ۰ بایت و HTTP ساده 502، بدون fallback، churn pool/مستقیم `kept=0`/`closed=1`، و D22 فقط با fixture محلی (درخواست در مقصد دو بار دیده شد)؛ **بخش ۳:** pin غلط روی یک عضو TLS واقعی ۰ درخواست در شمارندهٔ مقصد و `cooling:exit_unreachable` داد و کنترل با pin حذف‌شده ۱ درخواست/۲۰۰ داد (۱ عضو، بدون تعمیم)؛ **D22:** با gate و مقصد شمارندهٔ تحت کنترل کاربر، شمارنده برای یک CONNECT دو بار شمرد (`OK 2` به کلاینت) و با همان exit پین‌شده ۱ ماند و هیچ بایت بدنه نداد — یک مقصد/دو exit، بدون تعمیم؛ HTTP CONNECT از exit عمومی در بخش ۲ با ۱۱ تلاش CONNECT واقعی اجرا شد؛ UDP واقعی موضوعیت ندارد (v1 بدون UDP) |
| اجرای runtime روی arm64/riscv64/ppc64le و نصب واقعی + سرویس تحت RPM | **multi-arch: به‌تعویق‌افتاده (تصمیم کاربر ۲۰۲۶-۱۱-۲۱)؛ RPM-service: آزمون‌نشده** | فقط amd64 در runtime اجرا شده است (شامل payload RPM در container)؛ سه معماری دیگر فقط cross-build شده‌اند. کاربر runtime سه معماری را برای خود نه ارزشمند و نه امکان‌پذیر دانست؛ کار باز نیست و توسعهٔ نزدیک‌مدت محصول روی amd64 است. نصب واقعی + سرویس تحت RPM همچنان آزمون‌نشده و نیازمند محیط/مجوز جداست (`.github/workflows/ci.yml:147-173,199,239-250`) |

**تفکیک محدودیت‌ها (بازبینی و تصمیم کاربر، ۲۰۲۶-۱۱-۲۰؛ به‌روزشده ۲۰۲۶-۱۱-۲۱ و ۲۰۲۶-۱۱-۲۲):** سه حالت زیر از هم جدا می‌شوند و فقط ردیف (ب) «کار باز v1» است؛ گروه ج بخش ۱–۳ انجام شدند؛ TLS-pin عمومی روی یک عضو واقعی اثبات شد (بدون تعمیم) و HTTP CONNECT از exit عمومی در بخش ۲ پوشش داده شد؛ **wire-level replay روی pool واقعی در ۲۰۲۶-۱۱-۲۲ با gate/مقصد تحت کنترل کاربر اثبات شد** (یک مقصد/دو exit، بدون تعمیم؛ رکورد «وضعیت فعلی»). با این روش، عضو باز از گروه ج نمانده است؛ واریانت «exit تحت کنترل» آزموده نشد. این اثبات v1 تازه یا تغییر دامنه نیست.

**الف) خارج از v1 — پشتیبانی نمی‌شود (نه «آزمون‌نشده»):**
- **REALITY/ECH:** importer امنیت reality را `unsupported`/`ReasonRealityUnverified` برمی‌گرداند (`internal/imports/record.go:178-179`) و engine با پیام «REALITY is outside v1» رد می‌کند (`internal/engine/config.go:284-289`)؛ peer محلی فقط بلوک TLS ساده می‌سازد (`internal/acceptance/exit_test.go:98-113`).
- **mKCP:** در allowlist ترنسپورت نیست (`internal/imports/record.go:170-171`، `internal/engine/config.go:239-240`)؛ `kcp` نگاشت نمی‌شود (`record.go:283-288`) و peer generator برای آن panic می‌کند (`exit_test.go:73-97`)؛ UDP/QUIC هم سیاستاً ممنوع است (`docs/requirements.md` بخش «امنیت و موارد خارج از نسخهٔ اول»، `docs/architecture.md §۳`).
- **HTTPUpgrade:** همان حذف از allowlist (`record.go:170-171,283-288`)؛ سلول peer ندارد.
- **XHTTP روی h3/QUIC:** محصول ALPN دلخواه را می‌پذیرد (`internal/engine/config.go:238-241,330-332,403-425`) و ALPN peer پارامتری است (`exit_test.go:88-94,108-112`)، پس فقط یک سلول fixture کم است؛ ولی UDP/QUIC سیاستاً خارج از v1 است (`docs/requirements.md` بخش «امنیت و موارد خارج از نسخهٔ اول»، `docs/architecture.md §۳`) و اجراشده فقط h2 بوده (`internal/acceptance/transports_test.go:37`). بنابراین policy-excluded است، نه untested.
- **mux/XUDP:** عمداً حذف شده (`internal/imports/importer.go:80,91`، `internal/imports/sanitize.go:164-166`) و در قرارداد sidecar فیلدی ندارد (`internal/contract/sidecar.go:7-17`، `internal/singbox/config.go:62-70`).
- **Windows، UDP از pool، Android، GUI:** خارج از v1 با تصمیم کاربر (D24)؛ نیازمند milestone و تصمیم تازه‌اند. TUN در D29 به R16 تبدیل شده است و بخش v1 نیست؛ UDP از pool همچنان تصمیم جدا می‌خواهد.

**ب) پیاده‌سازی‌شده — وضعیت اجرا و کار باز v1 (به ترتیب):**
1. **reuse طولانی‌مدت ترنسپورت — انجام شد (۲۰۲۶-۱۱-۲۱):** سلول گیت‌شدهٔ `RL_ACCEPT_LONG_REUSE_TEST=1` در `internal/acceptance/reuse_test.go` یک تانل CONNECT روی WS/TLS (همان سلول اثبات‌شدهٔ R6) را باز می‌کند و سه درخواست keep-alive با بدنهٔ یکتا روی همان تانل می‌فرستد؛ پاسخ هر سه بایت‌به‌بایت برابر echo مبدأ و هر تلاش دقیقاً **۱ درخواست مقصد** (بدون تکرار/replay). **تعریف «طولانی‌مدت» از این پس همین پارامترهای تست است: ۳ درخواست، گسترهٔ زمانی ≥۳۰s و شکاف بی‌کار ≥۱۰s بین دو درخواست.** مشاهده: گسترهٔ ۳۲٫۰s، شکاف بی‌کار ۳۰٫۰s، **۱ اتصال خروجیِ مشتری** که در تمام پنجره زنده ماند و هر سه درخواست از آن رفت (۱ اتصال مقصد، بدون تحویل direct چون نام fixture روی host resolve نمی‌شود، بدون 502/timeout، سرویس پس از شکاف پاسخ‌گو). اولین probe سلامتِ زمان‌بندی‌شده (بررسی tick ۳۰s؛ چرخهٔ بعدی ۱۰ دقیقه) یک اتصال کوتاه‌عمر اضافی به exit ساخت (۰ تماس مقصد) و بسته شد؛ زمانِ آن اندازه‌گیری و به همان tick نسبت داده شد، و شمارش «برای این مشتری» از آن جدا شد و در PASS گزارش می‌شود. **شواهد (درخت کاری این تغییر، والد `ccdb4df`):** `gofmt -l internal/acceptance/reuse_test.go` خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./internal/acceptance/` با گیت خاموش سبز (۱۱۷٫۷s)؛ اجرای گیت‌شده **PASS در ۳۲٫۶۳s** (`RL_ACCEPT_LONG_REUSE_TEST=1 go test -count=1 -v -run TestTransportLongReuse ./internal/acceptance/`). commit محلی این تغییر: `test(acceptance): add gated long-duration transport reuse cell`. **قدم بعدی:** corrupt-state آفلاین زیر systemd که بعداً انجام شد (مورد ۵) و سپس session طولانی hysteria2.
2. **hysteria2 obfs — انجام شد (۲۰۲۶-۱۱-۲۱):** سلول گیت‌شدهٔ `RL_ACCEPT_HYSTERIA2_OBFS_TEST=1` در `internal/acceptance/r11_obfs_test.go` هر دو نوع مجاز را از مسیر واقعی محصول می‌آزماید: import لینک `hysteria2://…?obfs=salamander|gecko&obfs-password=…` از CLI، تولید بلوک top-level `obfs` در کانفیگ sidecar، و سپس CONNECT تأییدشده به مقصد fixture. سرور fixture همان باینری رسمی pinشدهٔ v1.14.4 است (archive sha256 `699a1b51…c24968`، commit `c005f353`) و بلوک `obfs` را کنار `users`/`tls` روی inbound hysteria2 می‌گذارد؛ این شکل پیش از اجرا با `sing-box check` همان باینری تأیید شد (هر دو نوع پذیرفته؛ `gecko` بدون رمز با `missing obfs password` رد می‌شود). **مشاهده:** هر دو حالت PASS — salamander **۱٫۳۵s** و gecko **۱٫۳۰s** (کل تست **۵۰٫۲۲s** با احتساب دانلود/بررسی digest؛ پکیج **۵۱٫۳۹s**)؛ هر کدام echo بایتیک‌سان، **دقیقاً ۱ تماس مقصد** و ۱ proof تأییدشده. دو حالت منفی (رمز اشتباه، و نوع اشتباه gecko در برابر salamander) هر کدام در **۱۱٫۴s** با **HTTP 504** و بدون payload، **۰ تماس مقصد**، و سرویس پس از شکست بالا و خوانا (`mode=targeted`، ۱ ردیف pool) — PASS. سلول فقط دو شکل fail-closed را می‌پذیرد: pool-timeout با `504 deadline exceeded` یا handoff خالی `200`؛ یک `502 dns failure` (که direct fallback نامحلول تولید می‌کند) یا هر پاسخ دیگر fail می‌شود. **شواهد:** `gofmt -l` روی دو فایل تغییرکرده خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./internal/acceptance/` با گیت خاموش سبز (**۱۲۲٫۰۴۷s**)؛ اجرای گیت‌شدهٔ بالا PASS. بدون تغییر رفتار محصول؛ تغییر harness فقط: پارامتر obfs اختیاری در helper سرور (`r11StartServer` → `r11StartServerWithObfs`) و جایگزینی بررسی no-op لایوینس با انتظار واقعی روی خروج پروسهٔ سرور. **یافتهٔ خارج از دامنه (اکنون رفع شد):** محصول رمز obfs را فقط برای salamander اجباری می‌کرد (`internal/imports/record.go:155-158`، `internal/engine/config.go:367-375`، `internal/singbox/config.go:232-240`) در حالی که `sing-box 1.14.4` برای gecko با رمز خالی هم `missing obfs password` می‌دهد؛ ورودی gecko بدون `obfs-password` در import پذیرفته می‌شد و کانفیگ تولیدشده را sidecar بارگذاری نمی‌کرد. طبق رفع ۲۰۲۶-۱۱-۲۱ در ابتدای «وضعیت فعلی» (دلیل `missing_obfs_password`)، import اکنون آن را رد می‌کند؛ برای رکورد قدیمی هم رفع ۲۰۲۶-۱۱-۲۱ در «تصمیم‌های باز» وضعیت مشتق‌شدهٔ `unsupported`/`missing_obfs_password` و کنارگذاری per-record از کانفیگ sidecar را اضافه کرد (state بازنویسی نمی‌شود) و validator سازندهٔ کانفیگ gecko بدون رمز را رد می‌کند. **قدم بعدی:** session طولانی hysteria2 (مورد ۳).
3. **session طولانی hysteria2:** آزمون‌نشده ولی شدنی است؛ به یک origin/exit محلی با عمر طولانی نیاز دارد.
4. **sidecar بستهٔ منتشرشده به‌جای باینری آزمون `/tmp` — انجام شد (۲۰۲۶-۱۱-۲۱):** `routelatch-v0.1.2-linux-amd64.tar.gz` و `SHA256SUMS` دوباره از ریلیز عمومی v0.1.2 دانلود شدند و فقط در `/tmp/opencode/rl-sidecar-proof` ماندند؛ چیزی نصب یا جایگزین نشد. sha256 فایل `SHA256SUMS` برابر `83b51b5b4f4b4d963b259de26c292963fb22276237df32908bc16ab1ea503bf2` (منطبق با digest اعلام‌شدهٔ همان دارایی در GitHub API) و sha256 تاربال برابر `fd6158bb7bb8973a9350e8419f6826066c276673c539e28ad7388d24ecb5de6e` (منطبق با `SHA256SUMS` و API؛ `sha256sum -c` = OK). محتویات: `bin/routelatch` با sha256 `5657903cc125c253756164afb8d8a0ebeae9e2cf6c98b0cd4e40f106e4363954` و `version` برابر `v0.1.2`/`88d04b1`، و `bin/sing-box` با sha256 `1264361b5f8868c8768f7555d3e77e20118bf503c41ac08bb290a0d99056aa3a` که **بایت‌به‌بایت** با باینری استخراج‌شده از آرشیو رسمی pin‌شدهٔ sing-box `v1.14.4` (archive sha256 `699a1b510aa4396b2176d32d1181920399ebf1e267835da6f3b598d0f7c24968`، commit `c005f353`) یکسان است؛ هر دو `1.14.4`/همان commit را گزارش می‌کنند و `SINGBOX-INFO` داخل بسته همین sha256 را تأیید می‌کند. سلول‌های گیت‌شده با همین sidecar اجرا شدند: `RL_ACCEPT_HYSTERIA2_TEST=1 RL_ACCEPT_HYSTERIA2_OBFS_TEST=1 RL_ACCEPT_SINGBOX_EXEC=<extracted>/bin/sing-box go test -count=1 -v -run 'TestR11Hysteria2RealTraffic|TestR11Hysteria2ObfsRealTraffic' ./internal/acceptance/` → **PASS**. `TestR11Hysteria2RealTraffic` **۴٫۶۱s**: echo بایت‌به‌بایت، **۱ تماس مقصد**، ۱ proof، exit مرده **502 با ۰ تماس جدید** (بدون direct)، مقصد تأییدنشده direct، D21، و بدون sidecar **502** — همه PASS. `TestR11Hysteria2ObfsRealTraffic` **۲۵٫۵۶s**: salamander **۱٫۳۳s** و gecko **۱٫۳۶s** هر کدام echo یکسان و **۱ تماس**؛ رمز اشتباه **۱۱٫۴۰s** و نوع اشتباه **۱۱٫۳۱s** هر کدام **HTTP 504 (deadline exceeded)** با **۰ تماس** و سرویس سالم — همه PASS؛ پکیج **۳۱٫۵۹s**. محصولِ تحت آزمون از همین درخت بازبینی‌شده ساخته شد و فقط `bin/sing-box` از تاربال آمد؛ از `bin/routelatch` تنها `version` خوانده شد. بدون تغییر کد محصول یا هارنس. **باقی:** فقط `amd64` آزموده شد (سه معماری دیگر و بسته‌های `.deb`/RPM نه)؛ session طولانی hysteria2 همچنان باز است (مورد ۳).
5. **corrupt-state زیر systemd — انجام شد (۲۰۲۶-۱۱-۲۱):** سلول گیت‌شدهٔ `TestA13SystemdCorruptState` در `internal/acceptance/a13_corrupt_state_test.go` (همان گیت `ROUTELATCH_SYSTEMD_TEST=1`) unit واقعی `deploy/systemd/routelatch.service` را با `--data-dir` و دو پورت تصادفی ایزوله اجرا می‌کند. ابتدا با state سالم بالا می‌آید، یک rule از طریق سرویس ثبت می‌کند، تمیز stop می‌شود و `backup` آفلاین می‌گیرد؛ سپس سه سناریو: JSON بریده (`{"schema_version":1,"mode":`)، JSON معتبر با `schema_version: 99`، و state غیرقابل‌خواندن (`chmod 000`). **مشاهده در هر سه:** unit به `failed` می‌رود با **`ExecMainStatus=10`** و **`NRestarts=0`** و `MainPID=0` (پس `RestartPreventExitStatus=10` از حلقهٔ restart جلوگیری می‌کند)؛ هیچ‌کدام از دو listener ترافیک bind نمی‌شود (پس هیچ درخواستی مسیر direct نمی‌گیرد)؛ `routelatch status` آفلاین exit `3` می‌دهد (هیچ managementی ادعای موفقیت نمی‌کند)؛ `doctor` آفلاین exit `10` با `kind=state_corrupt`؛ فایل **بایت‌به‌بایت دست‌نخورده** می‌ماند (۲۷ بایت برای JSON بریده) و mode هم عوض نمی‌شود؛ از `state.json.bak` هم بازگردانی خودکار نمی‌شود. `systemctl status` دلیل مستند را نشان می‌دهد: «state file is not valid JSON; it was not changed»، «state was written by a newer RouteLatch; refusing to guess its structure»، «state file cannot be read». **بازیابی مستند:** `restore <backup path>` آفلاین exit `0` و unit دوباره `active` با هر دو listener و همان rule؛ برای mode 000، chmod صریح `600` (تنها اقدام مجاز، بدون تغییر خودکار) باعث `doctor` exit `0` و شروع موفق با فایل بایت‌به‌بایت یکسان شد. **شواهد (درخت کاری این تغییر، والد `cdb99bd`):** `gofmt -l` روی فایل تغییرکرده خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./internal/acceptance/` با گیت خاموش سبز؛ اجرای گیت‌شده **PASS در ۲٫۰۰s** (اجرای نهایی روی همین درخت). **بازگردانی ماشین اثبات شد:** unit موقت و دایرکتوری‌های موقت حذف شدند، هیچ listener/unit موقتی باقی نماند، و سرویس زندهٔ کاربر پیش و پس `active` با همان `MainPID=142325`، `ExecMainStatus=0` و `NRestarts=0` و همان دو listener بود؛ هیچ state/config/proxy/DNS آن تغییر نکرد. **باقی:** فقط همین host/amd64؛ سناریو با unit موقت آزموده شد نه نصب واقعی کاربر؛ این سلول `backup`/`restore` را فقط آفلاین و روی backup خودش پوشش می‌دهد و بازیابی از `.bak`/فایل دستی را وعده نمی‌دهد (طبق قرارداد). **این ردیف snapshot تاریخی است؛ تست مذکور در R13-E بازنشسته شد و corrupt-state حالا آفلاین با `run --headless` پوشش داده می‌شود.**
6. **tick چرخش ۳۰ ثانیه‌ای:** اولین tick حالا در سلول گیت‌شدهٔ long-reuse مشاهده می‌شود (probe انتقالی ۳۰s، ۰ تماس مقصد)؛ چرخهٔ ۱۰ دقیقه‌ای و اعمال health همچنان در acceptance درایو نمی‌شوند.

**ج) اثبات‌نشده — محدود به محیط/مجوز (شامل اجراشده ولی محدود):**
- **گروه ج — بخش ۱–۳ انجام شد (۲۰۲۶-۱۱-۲۱) و D22 روی pool واقعی در ۲۰۲۶-۱۱-۲۲ اثبات شد (یک مقصد/دو exit، بدون تعمیم):** A04/A06 در مقیاس واقعی
  (import کامل pool، health/refresh واقعی، CONNECT به ۴ مقصد روی ۶ exit، churn و exit مرده)
  روی نمونهٔ ایزوله اجرا شد و اعداد و شاهدهایش در «وضعیت فعلی» ثبت است؛ **بخش ۳:** pin غلط
  روی یک عضو TLS واقعی ۰ درخواست در شمارندهٔ مقصد و `cooling:exit_unreachable` داد و کنترل
  با pin حذف‌شده ۱ درخواست/۲۰۰ داد (یک عضو، بدون تعمیم)؛ **D22:** در ۲۰۲۶-۱۱-۲۲ با gate و
  مقصد شمارندهٔ تحت کنترل کاربر، شمارنده برای یک CONNECT دو بار شمرد و کلاینت فقط یک `OK 2`
  گرفت؛ با همان exit پین‌شده شمارنده ۱ ماند و کلاینت هیچ بایت بدنه نگرفت (یک مقصد/دو exit، بدون تعمیم؛
  جزئیات در «وضعیت فعلی»). UDP واقعی موضوعیت ندارد چون v1 پشتیبانی UDP ندارد. تصمیم‌های کاربر (۲۰۲۶-۱۱-۲۱)
  برای هر سه بخش: اشتراک پرداختی و نیمه‌خصوصی (pool خصوصی وجود ندارد)؛ استفادهٔ
  فقط‌خواندنی؛ **سقف یک transport probe به‌ازای هر عضو pool**؛ هیچ ترافیک مقصدی در بخش
  ۱؛ churn فقط با mode switch/refresh؛ اجرا روی نمونهٔ ایزوله با data-dir و portهای
  اختصاصی تا سرویس زنده و پروکسی دسکتاپ دست‌نخورده بماند؛ هیچ لینک/credential/دادهٔ
  اتصال خصوصی در repo، fixture، لاگ یا گزارش نباشد.
- **اجرای runtime روی arm64/riscv64/ppc64le — به‌تعویق‌افتاده (تصمیم کاربر ۲۰۲۶-۱۱-۲۱):**
  فقط amd64 در runtime آزموده شده (شامل payload RPM در container)؛ سه معماری دیگر فقط
  cross-buildاند. کاربر این کار را برای خود نه ارزشمند و نه امکان‌پذیر دانست؛ کار باز
  نیست و توسعهٔ نزدیک‌مدت محصول روی amd64 است.
- **نشست GNOME برای A11 — به‌تعویق‌افتاده (اولویت پایین، تصمیم کاربر ۲۰۲۶-۱۱-۲۱):**
  KDE اثبات شده، GNOME نه؛ این یک محدودیت شناخته‌شده است، نه کار معلق این چرخه.
- **نصب واقعی + سرویس تحت RPM:** payload `x86_64` یک RPM در container یک‌بارمصرف نصب و اجرا شد (بخش اجرای payload RPM)، ولی systemd واقعی/شروع سرویس در container ممکن نبود و نصب روی host مجاز نیست؛ اجرای RPM روی arm64/سایر معماری‌ها هم آزمون‌نشده است.
- **QUIC roaming و رفتار با exit عمومی کند/ناپایدار:** QUIC roaming به تغییر واقعی مسیر/اینترفیس شبکه نیاز دارد و در repo بازتولیدپذیر نیست؛ رفتار با exit عمومی کند/ناپایدار در بخش ۲ فقط مشاهده‌ای دیده شد (چند exit پیش از بایت اول شکست خورد و طبق قرارداد cooling شد)؛ آزمون سیستماتیک آن انجام نشد.

این موارد مجوز ضعیف‌کردن D-item نیستند.

### تصمیم‌های باز (پیاده‌سازی‌نشده یا تازه‌بسته‌شده — نیازمند تصمیم/مجوز)

1. **وضعیت مؤثر برای رکوردهای ذخیره‌شدهٔ gecko بدون رمز obfs — انجام شد (۲۰۲۶-۱۱-۲۱):** قاعدهٔ مشتق‌سازی R5 حالا این رکورد را هم می‌گیرد. `EffectiveStatus` (مالک واحد) یک رکورد `hysteria2` با `obfs = gecko` و رمز خالی را صرف‌نظر از status ذخیره‌شده (جز `invalid`) به `unsupported` با دلیل پایدار **`missing_obfs_password`** مشتق می‌کند — همان رشته‌ای که import در `15d2acb` برمی‌گرداند. شرط یک‌بار در `contract.Hysteria2ObfsPasswordMissing` تعریف شده و import، view، probe و سازندهٔ کانفیگ sidecar همگی از همان استفاده می‌کنند. `pool.Dialable` این رکورد را رد می‌کند (پس `dialableRecords` آن را به engine نمی‌دهد)، probe انتقالی آن را بدون dial رد می‌کند، و `engine.prepareSidecar` هم به‌عنوان defense-in-depth آن را پیش از تولید کانفیگ کنار می‌گذارد، بدون افزودن به `Excluded` تا state بازنویسی نشود؛ مسیر `AddExit` هم همین رکورد را پیش از commit/Sync با خطای `unsupported` رد می‌کند تا گروه سالم handoff خود را از دست ندهد. exitهای سالم hysteria2 (gecko با رمز و hysteria2 بدون obfs) دست‌نخورده می‌مانند؛ اگر فقط رکورد خراب باشد sidecar کانفیگی نمی‌سازد و مسیر مستند «هیچ exit قابل استفاده نیست» (`pool_unavailable`، بدون direct) برقرار است؛ `singbox.Generate` هنوز هر exit غیرقابل استفاده را رد می‌کند. **شواهد:** تست‌های `TestEffectiveStatusDerivesStoredGeckoWithoutObfsPassword` (pool)، `TestStoredGeckoWithoutObfsPasswordDoesNotBreakTheGroup` و `TestAddExitRejectsStoredGeckoWithoutObfsPassword` (engine با `singbox.Generate` واقعی)، `TestStoredGeckoWithoutObfsPasswordIsIsolated` و `TestOnlyStoredGeckoWithoutObfsPasswordFailsClosed` (control؛ شامل برابری بایت‌به‌بایت state)، `TestTransportProbeSkipsStoredGeckoWithoutObfsPassword` (probes)، و `TestGenerateValidation`/`TestGenerateAcceptsGeckoWithPassword` (singbox) پیش از رفع روی HEAD `c5d48b4` (با تنها shim ثابت/helper قرارداد) fail دادند و پس از رفع pass شدند. `gofmt -l` روی فایل‌های تغییرکرده خالی؛ `go vet ./...` و `go build ./...` exit 0؛ `go test -count=1 ./...` سبز (۱۷/۱۷ پکیج، acceptance **۱۱۷٫۲۶۱s**، گیت‌ها خاموش). **بازبینی مستقل:** دور اول `Ready with nits` (یادداشت کهنهٔ پلن، نبود گارد `AddExit`، کامنت قرارداد) و پس از رفع، دور تأیید **Ready**؛ تست‌های هدفمند و `-race` روی دو تست engine gecko سبز. **باقی:** اگر گروه hysteria2 پیش از این رفع یک‌بار به‌خاطر همین رکورد fail کرده باشد، رکوردهای سالم آن گروه در state با `unsupported`/`sidecar_unavailable` ذخیره شده‌اند و طبق قاعدهٔ «پروتکل پشتیبانی‌شده status ذخیره‌شده را نگه می‌دارد» تا refresh/import دوباره خودکار برنمی‌گردند؛ این تغییر عمداً state را بازنویسی یا مهاجرت نمی‌کند. commit محلی همین تغییر (پیام: `fix(pool): derive stored gecko without obfs password as unsupported`).
2. **R13 — انتخاب فنی tray (۲۰۲۶-۱۱-۲۳):** library/implementation tray هنوز انتخاب و
   pin نشده؛ R13-A باید pure-Go بودن، static build، منو/نوتیفیکیشن، رفتار KDE و
   نبود watcher در GNOME/SSH را اثبات کند. اگر گزینهٔ قابل اتکا پیدا نشد، تغییر
   به cgo/باینری جدا یا تعویق tray تصمیم کاربر است؛ fallback بی‌صدا مجاز نیست.
3. **مورد دیگری در این بررسی یافت نشد.** این ادعا فقط تا همین بررسی اعتبار دارد.

### نقطهٔ شروع جلسهٔ بعد

1. `AGENTS.md` و همین بخش (مخصوصاً R15 برای تاریخ capability و R14 برای تاریخ انتشار) را
   بخوان؛ `git status` و `git log --oneline -5` را با HEAD ثبت‌شده مقایسه کن؛
   تغییر/دادهٔ کاربر را حفظ کن.
2. وضعیت مرجع (۲۰۲۶-۱۱-۲۶): **`v1.1.0` آخرین ریلیز عمومی است و R1–R15 بسته‌اند؛
   هیچ کار باز R15 یا v1 نمانده است.** **R16-A و R16-B در ۲۰۲۶-۱۱-۲۷ انجام و
   بازبینی شدند؛ verdict R16-A برابر `Ready` است و R16-C قدم بعد است. R16-I/J
   (DNS-through-pool و UDP) فقط task تحقیقی تعریف‌شده‌اند و بعد از H شروع
   می‌شوند.** `v1.1.0` سطح کنترل tray (R15)،
   `status.resume_requires_restart` و lifecycle restart سطح process را اضافه کرد:
   تگ annotated `7abe9704f66cbcc78fe6340093e8a365324b9407` روی
   `532c1697da6140fdfd5258dc98da2c1f7927cc61` (`532c169`، هر دو remote)، CI سبز
   `45796357850`، `Release` `45800238447` و Release عمومی id `459257636` با ۱۷
   دارایی و body آغازشده با «RouteLatch v1.1.0 — tray control surface». `v1.0.1`
   اولین ریلیز ساخته‌شده در CI است: تگ
   annotated `4462cc86` روی `d14da10` (هر دو remote)، CI سبز `45517940277`،
   `Release` `45521099138` (job انتشار `138895476159`) و Release عمومی با ۱۷
   دارایی‌ و digests منطبق. پیش از آن سه تلاش و یک دور رفع ثبت شد (gate
   annotated؛ draft با endpoint تگ 404؛ یادداشت‌های commit‌محور) و جزئیاتشان در
   همین بخش و `docs/release-readiness.md` است. **محدودیت‌های باز (کار v1
   نیستند):** پنجرهٔ create→id می‌تواند draft یتیم بگذارد؛ `check` صفحه‌بندی را
   با `per_page=100` فرض می‌کند؛ مسیر fail/cleanup فقط stub شده است؛
   `workflow_dispatch` اجرا نشده؛ و flakiness گاه‌به‌گاه step `Test` در CI دیده شد.
   محدودیت‌های R15-H (بدون اسکرین‌شات؛ Wayland/GNOME، desktop set/restore و
   autostart اجرانشده؛ proposal accept/recheck/clear و pool remove/test فقط با
   تست واحد؛ non-amd64 runtime-untested) در
   `spike/tray/evidence/r15h-acceptance-2026-11-26.md` است. HEAD فعلی `271964d`
   یک commit فقط‌مستند است؛ CI `45797624302` آن در job «Build, vet and test» و
   step `Test` قرمز شد در حالی که full suite محلی سبز است و این شکست به‌عنوان
   flake شناخته‌شده تأیید نشده است.
3. تصمیم D27: trigger انتشار، push تگ annotated `v*` است؛ هر ۱۷ دارایی در CI
   ساخته و پس از تأیید خودکار منتشر می‌شوند؛ هیچ ساخت/آپلود محلی در جریان
   ریلیز نیست. قاعدهٔ انتخاب batch در `AGENTS.md` و دروازهٔ CI سبز روی commit
   تگ دست‌نخورده‌اند.
4. ترتیب R14: (a) انجام شد (workflow + dry-run بدون انتشار؛ runهای
   `45397329232`، `45399453678`، `45400769014`، `45422318159` و dry-run نهایی
   `45463989732` روی `02e3226`؛ رفع یافته‌ها `952276e`/`02e3226`)
   → (b) بازبینی مستقل انجام شد (دور اول Not ready، دور دوم Ready with findings،
   تأیید نهایی بسته) و merge شد → (c) انجام شد: پس از حذف draft با مجوز کاربر،
   رفع `d14da10` بازبینی مستقل را گذراند، تگ روی همان commit بازسازی و `Release`
   `45521099138` منتشر شد. `workflow_dispatch` همچنان اجرا نشده است.
5. برای هر تغییر: YAML/shell parse، dry-run کامل، دو اجرای packaging با
   `SHA256SUMS` یکسان، `check-artifact.sh`/`check-package.sh`، و بازبینی مستقل.
   انتشار batch واجد شرایط طبق مجوز ایستادهٔ `AGENTS.md` (۲۰۲۶-۱۱-۲۶) بدون
   درخواست جدا مجاز است؛ هر تغییر میزبان مجوز جدا می‌خواهد.
6. شواهد را از `docs/contracts.md`، `packaging/README.md` و اجرای واقعی CI
   بردار، نه از حافظه؛ اگر رفتار GitHub Actions یا محدودیت runner با فرض‌ها فرق
   داشت، تصمیم را با کاربر باز کن و gate را بی‌صدا ضعیف نکن.
7. پایان هر task: ردیف R15/R16 (یا R14 برای تاریخ انتشار)، HEAD، checks واقعی و
   قدم بعدی را همین‌جا به‌روز کن. R15-0 تا R15-H و انتشار `v1.1.0` کامل شدند؛
   R16-A/B انجام شدند و R16-C قدم بعد است. موارد آزمون‌نشده (Wayland/GNOME،
   اسکرین‌شات، desktop set/restore، autostart، proposal accept/recheck/clear،
   pool remove/test، runtime غیر-amd64 و service install) فقط با مجوز/محیط جدا
   انجام می‌شوند و کار باز R15 نیستند.

### مسائل و حدود مجوز

- **تصمیم‌های کاربر ثبت‌شده (۲۰۲۶-۱۱-۱۸):** مسیر `/home/...` که در تاریخچهٔ `spike/` مانده
  با بازنویسی در یک کپی پاک می‌شود (تاریخچه‌ای را که product نگه می‌دارد بازنویسی نمی‌کنیم)؛
  بررسی رسمی نام/علامت فعلاً لازم نیست و نام حفظ می‌شود؛ ایمیل committer قابل‌انتشار است؛
  و push مجاز است. شاهد کامل در `docs/release-readiness.md`.
- **remoteها (۲۰۲۶-۱۱-۲۳):** `origin` یک remote با fetch از forgejo `routelatch.git`
  و دو push URL است: forgejo و GitHub `m-hosseinpour/routelatch.git`؛
  `git push origin main` هر دو مقصد را به‌روزرسانی می‌کند و remote جداگانهٔ `github`
  وجود ندارد. **فقط GitHub عمومی است**؛ تغییرهای قبلی که `github` را remote دوم
  ثبت کرده‌اند snapshot تاریخی‌اند.
- **مجوز/ممیزی:** D20 با Apache-2.0 بسته شد (R3)؛ source offer و بستهٔ انتشار در R8 فراهم
  شدند. D22 (نقطهٔ تعهد CONNECT) با موافقت کاربر ثبت شد.
- **مجاز بدون درخواست تازه:** build/test محلی، دریافت وابستگی عمومی، commit محلی،
  تغییر کد/مستندات پروژه.
- **نیازمند درخواست جدا:** نصب/فعال‌سازی سرویس (برای R7 انجام شد)، تغییر proxy/DNS/تنظیمات
  دسکتاپ، استفاده از اطلاعات اتصال خصوصی، آزمون خروجی‌های عمومی (اکنون در batch گروه ج)،
  و دستکاری پروژه‌ها/دادهٔ قبلی. push پس از یک batch تأییدشده، طبق قاعدهٔ
  تحویل `AGENTS.md` و تصمیم ۲۰۲۶-۱۱-۲۱، درخواست جدا نمی‌خواهد. tag/release طبق
  سیاست `AGENTS.md` و مجوز ایستادهٔ ۲۰۲۶-۱۱-۲۶ برای batchی انجام می‌شود که چیزی را
  که کاربر دریافت می‌کند تغییر می‌دهد (رفتار، قابلیت، بسته‌بندی، امنیت یا قرارداد
  کاربرمحور)؛ batch فقط‌مستند/شاهد push می‌شود و ریلیز نمی‌شود. هر انتشار همان
  دروازه‌های انتشار را دارد.
- **R15 و میزبان (۲۰۲۶-۱۱-۲۵):** کد/تست/build/commit محلی مجاز است. اجرای tray
  واقعی روی KDE با data-dir ایزوله (که یک آیتم tray و listener موقت می‌سازد) و
  تغییر واقعی پروکسی دسکتاپ و نصب/فعال‌سازی autostart/service مجوز جدا
  می‌خواهند. آزمون KDE نباید تنظیم میزبان را تغییر دهد؛ `desktop proxy set/restore`
  واقعی فقط با مجوز صریح و ترجیحاً در محیط ایزوله. D21 در کل R15 برقرار است.
- **تصمیم‌های کاربر برای گروه ج (۲۰۲۶-۱۱-۲۱، به‌روزشده ۲۰۲۶-۱۱-۲۲):** pool کاربر یک اشتراک
  **پرداختی و نیمه‌خصوصی** است و pool خصوصی وجود ندارد؛ استفادهٔ **فقط‌خواندنی** مجاز
  است. **بخش ۱:** حداکثر یک transport probe به‌ازای هر عضو pool، **هیچ ترافیک مقصدی**،
  و churn فقط با mode switch/refresh روی نمونهٔ ایزوله با data-dir/port اختصاصی. **بخش
  ۲:** مقاصد یک فهرست کوچک پایدار GETمحور است که هماهنگ‌کننده انتخاب می‌کند؛ IPv4/HTTP
  فقط‌خواندنی و بدون body حساس؛ UDP واقعی موضوعیت ندارد چون v1 پشتیبانی UDP ندارد. **بخش
  ۳:** یک عضو TLS واقعی با pin اشتباه و یک کنترل، هر کدام یک CONNECT؛ شمارندهٔ سمت مقصد از
  یک سرویس ثبت درخواست عمومی و بدون نگه‌داشتن token. **به‌روزرسانی ۲۰۲۶-۱۱-۲۲:** روش
  «exit تحت کنترل» لازم نشد؛ کاربر/هماهنگ‌کننده در ۲۰۲۶-۱۱-۲۲ صریحاً یک gate/مقصد تحت کنترل فراهم کرد که
  درخواست از exit واقعی forward می‌شود و پیش از پاسخ reset می‌کند. این همان پنجرهٔ
  pre-byte را فعال می‌کند؛ gate مقصد است، نه exit، و این روش در همان تاریخ اجرا شد.
  این تصمیم‌ها مجوز نصب سرویس، تغییر پروکسی/شبکهٔ میزبان یا خروجی سنگین نمی‌دهند.
- **تصمیم‌های کاربر (۲۰۲۶-۱۱-۲۰، به‌روزشده ۲۰۲۶-۱۱-۲۱):** ادامهٔ v1 در آن چرخه بدون
  گروه ج بود؛ در ۲۰۲۶-۱۱-۲۱ گروه ج به‌عنوان batch بعدی مجاز شد (A04/A06 در مقیاس
  واقعی، مقاصد دلخواه، churn، خروجی‌های عمومی واقعی، UDP واقعی، wire-level replay تا حد
  امکان؛ بخش ۳ در آن زمان نشان داد replay واقعی بدون exit تحت کنترل قابل اثبات نیست و بعداً در ۲۰۲۶-۱۱-۲۲ با gate/مقصد تحت کنترل کاربر اثبات شد). اشتراک کاربر پرداختی و نیمه‌خصوصی است؛ قواعد R4 عیناً برقرارند: فقط‌خواندنی،
  بدون لینک/credential/دادهٔ اتصال خصوصی در repo، fixture، لاگ یا گزارش، بدون بار سنگین
  روی نودهای مشترک، و روی نمونهٔ ایزوله با data-dir و portهای اختصاصی بدون تغییر سرویس
  زنده/تنظیم پروکسی دسکتاپ. سقف بار و فهرست مقاصد در تصمیم گروه ج بالا تثبیت شد.
  Windows، UDP از pool، Android و GUI «خارج از v1»اند؛ TUN در D29 به R16 (پس از
  v1) منتقل شد. A01 با شهادت کاربر بسته شد؛ runtime
  سه معماری و GNOME به‌تعویق افتادند. corrupt-state آفلاین زیر unit موقت در ۲۰۲۶-۱۱-۲۱
  انجام و مجوزش مصرف شد؛ سایر اقدام‌های میزبان مجوز جدا می‌خواهند.
