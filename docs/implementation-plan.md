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
- **کار:** مجوز D20 را با کاربر ببند؛ license/source notices و dependency audit actual
  build، artifact integrity و به‌روزرسانی docs/README. نام/registry/trademark دوباره
  بررسی شود؛ public repository/publish/push نیاز به مجوز جدا دارد.
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
| A14 | T7، مسدود تا تصمیم مجوز و ممیزی build |

دادهٔ dev ساخته‌شده برای بازبینی دستی حذف نشود. import قدیمی read-only؛ upgrade
از state قبلی backup خصوصی و schema version داشته باشد. rollback موتور و state
باید compatible باشد؛ حذف broad یا reset برای تست ممنوع است. credential در commit،
fixture، report یا سرویس بیرونی قرار نگیرد.

<a id="open-work"></a>

## کارهای باز و وضعیت ادامه

این بخش مرجع واحد ادامهٔ کار در جلسه‌های بعد است؛ وضعیت در `AGENTS.md` یا سند
جداگانه تکرار نشود. هنگام شروع/پایان task، وضعیت، شاهد check و قدم بعدی اینجا
به‌روز شوند. درخواست جدید «کارهای باز را شروع کن» برای شروع task رفع‌مانع‌شده
در محدودهٔ مستند آن کافی است؛ تأیید تکراری صرفاً به‌خاطر قدیمی‌بودن این رکورد لازم
نیست. اجازهٔ نصب سرویس، تغییر شبکه، استفاده از credential یا انتشار جداست.

### وضعیت و شواهد فعلی

- ایجاد پوشه و مستندات: انجام‌شده؛ لینک‌ها و شناسه‌ها بررسی شده‌اند. بازبینی مستقل
  اولیه «آماده برای کارهای رفع‌مانع‌شده» گزارش کرد؛ چهار یافتهٔ مادی در handshake،
  pooled/hijacked connections، مرز NO_PROXY و fixture مخرب اصلاح شدند. بازبین در
  بررسی متمرکز هر چهار یافته را بسته و مستندات را آماده گزارش کرد؛ این نتیجه
  آمادگی اجرای T1، صحت runtime یا آماده‌بودن پیاده‌سازی را اعلام نمی‌کند.
- راه‌اندازی Git محلی روی `main` و افزودن قواعد ماندگار مخزن: انجام‌شده؛ بازبین
  مستقل مستندات و ارجاع ادامهٔ کار را آماده گزارش کرد. بررسی هفت سند، ۲۶ لینک داخلی،
  anchor ادامهٔ کار، ۲۰ تصمیم و ۱۴ معیار پذیرش موفق بود؛ `git diff --cached --check`
  نیز خطای whitespace نداشت. هیچ remote، سرویس یا کد اجرایی ایجاد نشده است.
  checkpoint و مشخصات commit این تغییر از تاریخچهٔ مخزن (`git log`) قابل بازیابی‌اند؛
  hash در خود سند تکرار نمی‌شود.
- **T1 اجرا شد (۲۰۲۶-۱۱-۱۷).** toolchain واقعی: Go 1.27.4 محلی با checksum رسمی؛
  هستهٔ pin‌شده `github.com/xtls/xray-core`
  `v1.260327.1-0.20261109052709-676eaf66f8f0` (کامیت tag `v26.11.9`؛ خودِ tag به‌عنوان
  نسخهٔ ماژول Go رد می‌شود). شاهد runtime: `core.Dial` در ۲۳µs با `nil` برمی‌گردد در
  حالی که مقصد ۵۰۲ms بعد وصل می‌شود؛ با خروجی مرده هم `nil` و صفر تماس با مقصد. خطاهای
  مقصد (refused/DNS/FIN/RST) به `EOF` جمع می‌شوند؛ teardown پیش‌فرض ~۱s و با سیاست
  خروجی سریع ۲٫۵–۳ms؛ gRPC/XHTTP بعد از `Stop` باقی می‌مانند؛ balancer خالی به default
  handler و مسیر direct می‌رسد. جزئیات و commands در `spike/xray/FINDINGS.md`.
- **دروازهٔ handshake عبور نکرد و با تصمیم صریح کاربر (۲۰۲۶-۱۱-۱۷) بسته شد:** تأیید
  واقعی هر جا پروتکل می‌دهد (خروجی SOCKS5/HTTP با reply) و برای VLESS/Trojan پذیرش
  صریح و قابل‌مشاهدهٔ «خروجی پذیرفت» بدون ادعای دسترسی مقصد؛ نبود خروجی سالم همچنان
  خطای روشن پیش از reply است و fallback مستقیم در هیچ مسیری وجود ندارد.
- **frontend:** `goproxy v1.9.4` رد شد (بازنویسی پاسخ، بافرکردن stream، گم‌شدن بایت‌های
  pipelined CONNECT، بازپخش یک GET روی اتصال reused)؛ `go-socks5 v0.1.4` فقط با resolver
  بدون lookup و `WithDialAndRequest` پذیرفته شد؛ UDP/BIND باید `0x07` بگیرند. شاهد در
  `spike/frontend/FINDINGS.md`. هر دو واحد بازبینی مستقل داشتند و یافته‌های مادی بسته شد.
- **تصمیم‌های کاربر در همین تاریخ:** ماتریس v1 = لینوکس amd64+arm64 با پروکسی دسکتاپ
  GNOME/KDE (بقیه محیط‌ها پیام صریح). برای D18: هستهٔ pin‌شده `allowInsecure` را حذف
  کرده است، پس مسیر رضایت در نسخهٔ اول وجود ندارد؛ رکورد insecure قرنطینه و `unsupported`
  با دلیل `insecure_tls_removed` می‌ماند و لینک دارای pin گواهی با verification روشن
  پذیرفته می‌شود (تصمیم واگذارشده به هماهنگ‌کننده، ۲۰۲۶-۱۱-۱۷).
- **T3/T4/T5/T6 (پیاده‌سازی) پیش رفت و متوقف شد (۲۰۲۶-۱۱-۱۷، به درخواست کاربر).**
  هفت پکیج پیاده و با آزمون واحد سبز شدند: `internal/{routing,engine,conns,frontend,
  pool,imports,probes,diagnostics,proposals,platform,store,control}` و `cmd/routelatch`
  (۱۶ فرمان / ۳۴ فرم برگ، JSON و exit code طبق §7). کل درخت با هم `go build`، `go vet` و
  `go test` سبز است. اعداد اندازه‌گیری‌شده: close p99 120–442µs، cancel p99 62–98µs، پس از
  Stop بدون باقیماندهٔ fd/goroutine؛ `core.Dial` همچنان ۳۲–۱۹۷µs پیش از تماس مقصد برمی‌گردد
  و همین دلیل proof class `ProofAccepted` برای VLESS/Trojan است.
  **مهم:** این وضعیت «پیاده‌شده و آزمون واحد» است، نه پذیرش. هیچ جریان کامل کاربر
  (install → import → rule → use → proposal → mode switch → pool failure → all-direct →
  stop) هنوز اجرا نشده و A01–A13 پذیرفته‌نشده‌اند.
- **اصلاحیهٔ قرارداد (۱۲ شکاف موج اول) بسته شد:** کد `NotFound`/`ExternalToolFailed`,
  `Proposal.Kind`/`RuleID` با ماندگاری پیشنهاد unroute، انتساب نتیجهٔ probe،
  `Registry.SetPolicyVersion` و حذف شمارنده‌های بی‌نویسنده؛ شش اصلاح متنی در
  `docs/contracts.md`. بازبینی مستقل approve داد (چهار یافته رفع شد).
- **پوستهٔ برنامه پیاده شد:** `internal/store` (اتمیک، lock تک‌نویسنده، migration،
  backup/restore، corrupt → fail-closed)، `internal/control` (socket 0600 با SO_PEERCRED،
  سقف درخواست/پاسخ، exit code از contract) و `cmd/routelatch` (۱۶ فرمان/۳۴ فرم، JSON،
  منو، `run`، doctor). بازبینی مستقل بدون یافتهٔ مسدودکننده.
  گپ باقی‌مانده: مسیر accept/reject پیشنهاد unroute فقط source-traced است، چون تولیدکنندهٔ
  آن در جریان عادی وجود ندارد؛ در T7 آزموده شود.
- **T7 (پذیرش) شروع شد و سه نقص واقعی پیدا کرد (۲۰۲۶-۱۱-۱۷):**
  F1 تونل pool بعد از reply می‌مرد (context با `defer cancel()` عمر session را می‌بست؛
  HTTP 200 سپس EOF، SOCKS `0x00` سپس ۰ بایت)؛ F2 خطای cooldown probe به `internal`/exit 1
  نگاشت می‌شد؛ F3 `fenceMu` کل dial را قفل می‌کرد و mode switch تا deadline (~9.8s) می‌ماند.
  هر سه رفع و بازبینی شدند؛ اکنون mode switch ۰٫۱۳–۰٫۳۷ms و بستن dial در جریان
  ۰٫۱۶–۰٫۴۳ms است. harness: ۸۴ pass/۵ fail → ۸۹ pass/۰ fail.
  **درس ثبت‌شده:** آزمون واحد پکیج‌ها این کلاس (مالکیت lifetime و seam بین‌پکیجی) را
  نمی‌گیرد؛ harness جریان کامل باید بخشی از تعریف «انجام‌شده» بماند.
- **T2 بسته شد:** `docs/contracts.md` (مدل مقصد، proof class و ماتریس حالت، تاکسونومی خطا،
  state/مدیریت، pool/import/sanitization، probe/سلامت، CLI/menu، نگاشت A01–A14) و
  `internal/contract` یخ‌زده با ۲۲ kind خطا (پس از اصلاحیه). بازبینی مستقل ۱۱ یافتهٔ مادی و ۲ رگرسیون
  را یافت و همه بسته شدند. تست‌های دورانداختنی مسیر مقصد (شامل `[::]:80` و PSL) پاک
  شدند؛ آزمون دائمی این رفتار در T3 است.

| کار | وضعیت | شرط شروع/ادامه |
| --- | --- | --- |
| T1 — آزمایش frontend/Xray | انجام‌شده؛ دروازهٔ handshake با تصمیم کاربر بسته شد | شاهد runtime و محدودیت‌های transportها در `spike/` ثبت شده است |
| T2 — قراردادهای فنی | انجام‌شده؛ بازبینی مستقل بسته شد | `docs/contracts.md` و `internal/contract` مرجع‌اند؛ تغییر interface نیازمند هماهنگ‌کننده |
| T3 — مسیر TCP و اتصال‌ها | پیاده و آزمون واحد؛ پذیرش یکپارچه نشده | مسیر routing/engine/frontend با peerهای loopback سبز است |
| T4 — pool و import | پیاده و آزمون واحد؛ پذیرش یکپارچه نشده | fixture مخرب و refresh اتمیک آزموده شده؛ خروجی عمومی آزموده نشده |
| T5 — پیشنهادها و مدیریت | پیاده (diagnostics/proposals/control/CLI/menu)؛ پذیرش یکپارچه نشده | must: no-rule-without-approval و no-replay |
| T6 — نصب و سرویس | فایل‌های unit/launcher/desktop آماده؛ نصب واقعی انجام نشده (نیاز به مجوز جدا) | آزمون با runner جعلی، بدون تغییر تنظیم واقعی |
| T7 — پذیرش و انتشار | در جریان: harness ۸۹ pass / ۰ fail پس از رفع F1–F3؛ گسترش پوشش A-itemهای باقی‌مانده در جریان | A01/A14 و systemd/دسکتاپ واقعی/خروجی عمومی قابل‌آزمون نیستند؛ انتشار مسدود تا D20 |
| Windows / UDP / TUN / Android / GUI | شروع نشده؛ هنوز پلن اجرایی ندارند | تصمیم scope و دروازه‌های milestones بعدی |

T1 اجرا شد و دروازهٔ handshake با تصمیم صریح کاربر بسته شد؛ T2 در جریان است. مرزهای
آزموده‌نشده‌ای که v1 را تهدید می‌کنند: HTTP CONNECT exit، TLS-pin failure، REALITY/ECH،
mKCP، HTTPUpgrade، XHTTP h3، mux/XUDP و رفتار با خروجی عمومی. این‌ها محدودیت ادعا هستند،
نه مجوز ضعیف‌کردن D-item.

### وضعیت پذیرش (آخرین اجرای loopback، ۲۰۲۶-۱۱-۱۷)

`go test -count=1 -v ./internal/acceptance/` → ۱۵۷ بررسی، ۰ شکست (~۱۲۰s). این جدول
«محلی اثبات‌شده» را از «پذیرفته‌شده» و «قابل‌آزمون نیست» جدا می‌کند:

| معیار | وضعیت | حد شاهد |
| --- | --- | --- |
| A02، A03، A05، A06، A08، A12 | اثبات محلی | قاعده/مسیر/پروتکل با شمارندهٔ تماس مقصد و خروجی |
| A04، A07، A09، A10، A11، A13 | اثبات محلی (بخشی) | mode switch زنده+در جریان، rerank/cooldown، مجوز فایل/socket، CLI، launcher env، crash/restart |
| A01 | قابل‌آزمون نیست | نیاز به ماشین پاک و نصب واقعی؛ باینری استاتیک و `env -i` شاهد جزئی است |
| A11 (تنظیم واقعی دسکتاپ) | قابل‌آزمون نیست | تغییر تنظیم دسکتاپ مجوز جدا می‌خواهد؛ فقط runner جعلی |
| A13 (systemd واقعی) | قابل‌آزمون نیست | نصب/فعال‌سازی سرویس مجوز جدا می‌خواهد |
| A14 | مسدود | تصمیم مجوز (D20) و ممیزی وابستگی build واقعی |
| A04/A05/A12 (خروجی عمومی، replay در سطح wire، UDP واقعی) | قابل‌آزمون نیست | خروجی عمومی مجوز جدا و خارج از loopback است |

نتیجهٔ صادقانه: نسخهٔ لینوکسی در محدودهٔ مجاز (ساخت/آزمون محلی) پیاده و تا حد
قابل‌اثبات آزموده است؛ A01/A11-دسکتاپ/A13-systemd/A14 بدون مجوز یا تصمیم جدید
«پذیرفته‌نشده» می‌مانند و نباید passed گزارش شوند.

### نقطهٔ شروع جلسهٔ بعد

1. درخواست جاری کاربر و `AGENTS.md` را بخوان؛ `git status` و این رکورد را با وضعیت
   موجود مقایسه کن. تغییرات یا دادهٔ کاربر را حفظ کن. اگر verdict بازبینی اصلاحیهٔ
   قرارداد رسیده، آن را ببند و checkpoint قابل‌ساخت بساز.
2. کار بعدی = **T7 (پذیرش)**: یک harness محلی که جریان کامل کاربر را روی peerهای loopback
   اجرا کند (install/import/rule/use/proposal/mode switch/pool failure/all-direct/stop)،
   سپس leak/counter checks، سقف منابع، crash/cancel و secret scan. نصب سرویس، تغییر
   پروکسی دسکتاپ، خروجی عمومی و انتشار همچنان مجوز جدا می‌خواهند.
3. شواهد T1 (`spike/*/FINDINGS.md`) و قراردادها مرجع‌اند؛ تعدادهای teardown/proof class را
   از آن‌جا بردار، نه از حافظه یا تحقیق قدیمی. هیچ تضمین D-item با «موقتاً» ضعیف نمی‌شود.
4. اگر فقط یک سؤال وضعیتی مطرح شده، پاسخ بده و کاری را خودکار شروع نکن. مانع
   فنی/مجوزی واقعی را مشخص کن؛ اجازهٔ تازه را فقط برای محدودهٔ نامجاز بخواه.
5. نتیجهٔ واقعی هر task، نسخه‌ها، commands، checks، محدودیت‌ها و task بعدی را در همین
   بخش ثبت کن؛ اگر شکست خورد، تصمیم وابسته را باز کن و تضمین را ضعیف نکن.

### مسائل و حدود مجوز

- تصمیم‌های باز: D20 مجوز (انتشار مسدود)؛ ممیزی وابستگی واقعی build (A14).
- درخواست جاری: اجرای پلن تا نسخهٔ اول لینوکسی، شامل build/test محلی، دریافت
  وابستگی‌های عمومی و commit محلی، مجاز است.
- خارج از مجوز (نیاز به درخواست جدا): نصب/فعال‌سازی سرویس، تغییر proxy/DNS ماشین،
  استفاده از اطلاعات اتصال خصوصی، آزمون خروجی‌های عمومی، remote/push/انتشار، و
  دست‌کاری پروژه‌های قبلی.
