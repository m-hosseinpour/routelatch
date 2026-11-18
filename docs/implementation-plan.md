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
> برای کارهای R1–R10 آمدهاند مربوط به قبل از بازنویسیاند و و در مخزن عمومی وجود
> ندارند. مرجع معتبر، HEAD انتشارشده است.
  (۲۰۲۶-۱۱-۱۸؛ HEAD فعلی `b01661d`. بررسیها روی معادل قبل از بازنویسی `3425c3f` اجرا و روی `3cb7bd7` پس از بازنویسی تأیید شد —
هنگام شروع، `git log` را ببین)
   **بازخوانی سرد ۲۰۲۶-۱۱-۱۸ (ارکستریتور):** `git status` پاک و HEAD واقعی `b01661d` بود — دو commit صرفاً سندپسند (`4106da5` بستن ادعای کهنهٔ انتشار، `b01661d` ثبت R11) بالای `efc73c3` که رکورد به آن اشاره میکرد. تنها HEAD بهروز شد؛ هیچ کد/سرویس/تنظیم میزبان تغییر نکرد. commit بازخوانی: `a18dd97`.)

- **بررسی R11 (۲۰۲۶-۱۱-۱۸، ارکستریتور — تحقیق خواندنی فقط؛ بدون مجوز و بدون تغییر کد):** دو عامل مستقل وارد تقسیمشدند، یکی مسیر import/اعتبارسنجی/D-item و دیگری موتور/رانتایم. شواهد سورس: `hysteria2://` در `imports/link.go:41-46` به `unsupported`/`protocol_unsupported` و تنظیمات sing-box در `v2rayn.go:29-34,80` به `invalid` میافتد؛ نقاط تصمیم پروتکل پراکندهاند (`contract/pool.go:16-33`، `imports/{link,v2rayn,record}.go`، `engine/config.go:176-232,343-409`). Xray pinشده (`v26.11.9`، `go.mod:8`) تنها `proxy/hysteria` (هیستریا **v1**) دارد و آن outbound در `main/distro/all/all.go` ثبت نشده و outboundای به نام `hysteria2` در tree نیست → «گسترش موتور pinشده» بهتنهایی R11 را برآورده نمیکند. پشتیبانی واقعی یا رانتایم دوم (sing-box) یا مهاجرت تکموتوره میخواهد؛ جداسازی process-global دو core آزموننشده است (`spike/xray/FINDINGS.md:225`) و اسکیمای sing-box در `docs/research.md` شاهدی ندارد. **پایان تحقیق است، نه شروع R11:** تصمیم معماری + مجوز صریح کاربر اخذ نشده و D-itemها (no MITM، preservation hostname، fail-closed، quarantine) تغییرناپذیراند. آزمون/harness آینده: `internal/acceptance/a06_imports_test.go` (مالک import) و الگوی peer محلی `fixtures_test.go`/`peers_test.go`/`exit_test.go:185`.
   **تصمیم + مجوز کاربر (همان جلسه):** معماری «رانتایم دوم (sing-box) کنار Xray» پذیرفته شد — Xray فعلی برای مسیرهای اثباتشده میماند و sing-box فقط exitهای hysteria2/sing-box را هدایت میکند. D-itemها تغییرناپذیرند. **دروازهٔ قبل از کد:** مجوز sing-box (GPL-3.0?) و اسکیمای معتبر hysteria2 outbound باید از منبع رسمی تأیید شود؛ چون D20 با Apache-2.0 بسته شده، تعارض مجوز احتمالی پیش از هر تغییر کد با کاربر باز میشود.
   **نتیجهٔ دروازه (۲۰۲۶-۱۱-۱۸، دو عامل پژوهشی، منابع رسمی pinشده):** sing-box `v1.14.4` (tag ۲۰۲۶-۱۰-۲۴، commit `c005f353`) تحت **GPL-3.0-or-later** + بند ممنوعیت نامگذاری مشتقات است (`LICENSE` سورس رسمی؛ GitHub `spdx_id=NOASSERTION`) و `SagerNet/sing` هم GPL-3.0 است. در مقابل هستهٔ hysteria2 `github.com/apernet/hysteria/core/v2` در tag `core/v2.14.0` **MIT** است. طبق GPL-3.0 §5(c)/§0، جاسازی sing-box در باینری RouteLatch کل باینری را تحت GPL میبرد و با D20/Apache-2.0 و ممیزی «صفر GPL» تعارض دارد؛ ارسال sing-box بهعنوان **executable جدیگانه** (aggregate، ارتباط arm's-length از طریق socket) این تعارض را حل میکند ولی source offer + notices + رعایت بند نامگذاری و تغییر بستهٔ release را اضافه میکند. اسکیمای معتبر: outbound `hysteria2` در `option/hysteria2.go` فیلدهای `server`/`server_port`/`password`/`tls`(اجباری)/`up_mbps`/`down_mbps`/`obfs` و **بدون `transport`** دارد؛ **هیچ RPC اضافه/حذف outbound در لحظه وجود ندارد** (`daemon/started_service.proto`) → رانتایم دوم با regenerate+reload تنظیم میشود؛ handoff محلی از طریق inbound `mixed`/`socks` و حفظ hostname با حذف قواعد `resolve`؛ URI `hysteria2://`/`hy2` با پورت پیشفرض ۴۴۳ و پارامترهای `obfs`/`obfs-password`/`sni`/`insecure`/`pinSHA256`/`ech` و پهنای باند باید توسط RouteLatch تنظیم شود (از URI خوانده نمیشود). **کد هنوز ننوشته است.** **انتخاب نهایی ارکستریتور (با تفویض صریح کاربر «بهترین حالت رو انتخاب کن»، ۲۰۲۶-۱۱-۱۸):** مسیر ۱ — sing-box `v1.14.4` بهعنوان **executable جدیگانهٔ GPL-3.0** (process جدیگانه، ارتباط arm's-length از طریق inbound محلی) ship میشود؛ باینری RouteLatch Apache-2.0، LICENSE/THIRD-PARTY-NOTICES و ممیزی ۶۸ ماژول تغییر نمیکنند. تعهدات اضافی: notices/source offer GPL-3.0 + بند نامگذاری + بستهٔ release حاوی باینری دوم. مسیر ۳ (جاسازی + تغییر مجوز کل باینری) رد شد چون مخزن عمومی قبلاً تحت Apache-2.0 منتشر شده و بازپسگیری آن مجوز معتبر نیست. D-itemها دستنخوردهاند.)

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
  نکرد و هیچ‌چیز نصب‌شده نماند. **اجرا نشد:** reboot/re-login و corrupt-state زیر systemd
  (فقط offline). A01 همچنان پذیرفته‌نشده است (نصب روی این ماشین اثبات شد، نه محیط پاک).
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
  **شروع در login فقط config-verified است** (reboot به درخواست کاربر اجرا نشد).
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
- **شاهد پذیرش:** `go test -count=1 -v ./internal/acceptance/` روی همین HEAD →
  **۲۱۵ گزارش `pass(...)`، ۰ شکست و ۱۳ `NOTE`** (اجراهای گیت‌شده/غیرقابل‌اجرا، نه pass
  جعلی)، ~۱۱۶s؛ ۱۶/۱۶ پکیج `go test -count=1 ./...` سبز. `go build ./...`،
  `go vet ./...` و tree پاک. بستهٔ release روی همین HEAD از یک export پاک ساخته و
  تأیید شد (`routelatch-e098ceb-linux-amd64/arm64`، stamp هم‌خوان BUILD-INFO،
  `sha256sum -c` OK)؛ بسته فقط از tree پاک ساخته شود، چون stamp با tree dirty
  `-dirty` می‌شود. پایه ۲۰۸ بود؛ R10 شش assertion اضافه کرد و عدد ۲۱۵ توسط همان worker تأیید شد.
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
| R1 | **انجام شد** (`e168512`, `b5b155e`, `5f293e8`, `596529f`): unit واقعی `systemd --user` نصب و lifecycle آن اجرا شد — enable/start/restart/stop، `ExecMainStatus=0`، `/proc/PID/exe` باینری نصب‌شده، دو listener فقط loopback، socket کنترل `0600` در dir `0700`؛ port collision واقعی با `ExecMainStatus=6`/`NRestarts=0`/CLI exit 3 و بدون fallback؛ `kill -9` → restart ~۲s با `state.json` سالم و بدون direct پنهان (exit خاموش → 502 با ۰ تماس)؛ اجرای دوم exit 4 و loop شروع exit 7. دو نقص بریده‌شدن پیام رفع شد و `TestA13SystemdUserLifecycle` (گیت `ROUTELATCH_SYSTEMD_TEST=1`) اضافه شد. **ماشین به وضعیت قبل بازگشت و هیچ چیزی نصب نماند. اجرا نشد:** reboot/re-login و corrupt-state زیر systemd | A13 (اثبات واقعی) و مسیر نصب A01 | انجام شد با مجوز شما |
| R2 | **انجام شد** (`72835fc`) روی KDE Plasma واقعی: آداپتر `kwriteconfig6`/`kioslaverc`، set واقعی روی OS، ثبت مقدار قبلی، restore با **diff بایت‌به‌بایت** به snapshot، ویرایش دستی کاربر حفظ و `user_modified` گزارش شد، سرویس خاموش **fail closed** (۰ تماس مقصد)، `kill -9` → restart ~۲s با عبور ۲۰۰. نقص ادعای مالکیت کهنه در `status` رفع شد؛ `TestA11DesktopProxyLifecycle` (گیت `ROUTELATCH_DESKTOP_TEST=1`) زنده pass شد. **اجرا نشد:** نشست واقعی GNOME؛ `ftpProxy`/`Authmode` طبق تصمیم مدیریت نمی‌شوند | A11 (KDE) | انجام شد |
| R3 | **انجام شد** (`7792cac`، اصلاحیه‌های notices/attribution `4e0dde6`، `61604c5`): D20 با Apache-2.0 بسته شد؛ `LICENSE`/`NOTICE`/`THIRD-PARTY-NOTICES.md` و ممیزی ۶۸ ماژول و ۶ کامپوننت embedشده (amd64+arm64، `go version -m` هم‌خوان) اضافه شدند. صفر مجوز ناشناخته؛ MPL-2.0 فقط dependency بدون تغییر؛ LGPL-3.0 با استثنای لینک استاتیک و متن GPL-3.0؛ جزئیات و محدودیت‌ها در `docs/licensing.md`. **باقی A14:** source offer برای MPL، بستهٔ سورس/انتشار و بازبینی امنیت/نام — بیرون از این تصمیم | A14 (بخش مجوز/ممیزی) | انجام شد — تصمیم شما ثبت شد |
| R4 | **انجام شد** (`766edcd`, `da333fe`) با pool خصوصی کاربر (فقط‌خواندنی؛ هیچ secret در repo/گزارش): نمونهٔ ۴ لینک از ۱۶۵ خط، import ۳/۳ valid + ۱ `engine_record_rejected` مستند، transport ۳/۳ و health/ranking اندازه‌گیری‌شده، یک درخواست targeted با CONNECT 200 و **۱ تماس مقصد** و IP خروجی ≠ مستقیم، exit مرده → 502 بدون fallback، all-direct/stopped مطابق قرارداد، و grep D21 بدون هیچ hit مقصد. نقص «شواهد downlink واقعی به health نمی‌رسید» رفع شد. **باقی:** مقاصد دلخواه، churn/طولانی‌مدت، و همهٔ اعضای pool | A04/A06 (ردیف عمومی، اثبات محدود) | انجام شد |
| R5 | **انجام شد** (`640f83c`, `5d328a6`): مسیر import هرگز `unknown` نمی‌سازد (http → `unsupported`/`http_exit_not_verified`، scheme ناشناس → `unsupported`/`protocol_unsupported`) و view/status حالا رکورد undialable را **صرف‌نظر از status ذخیره‌شده** جز `invalid` مشتق می‌کند. `pool list`، `status` و `doctor` از یک مالک واحد (`EffectiveStatus`) می‌خوانند، state بازنویسی نمی‌شود و رکورد پیش از هر dial/probe رد می‌شود. **Residual پذیرفته‌شده:** protocol تهی/صفر عمداً دست‌نخورده است؛ semantics پروتکل‌های پشتیبانی‌شده تغییر نکرده؛ tick چرخش ۳۰ ثانیه‌ای در acceptance درایو نمی‌شود؛ `SyncStatuses` پس از فرمان تغییردهنده ممکن است cooldown منقضی را به `unknown` نرمال کند (bookkeeping قبلی، نه نوشتن مشتق‌سازی) | A06/§5 (کیفیت گزارش) | نه |
| R6 | **انجام شد** (`a69ae1e`): WS/TLS، gRPC/TLS (`h2`) و XHTTP/TLS (`h2`) با exit pin‌شدهٔ محلی، rule، `pool select` و CONNECT به peer loopback واقعاً آزموده شدند (هر سلول دقیقاً ۱ درخواست مقصد و پاسخ بدون تغییر)؛ pin غلط ۰ داده به مقصد می‌فرستد و سرویس بالا می‌ماند. **هنوز آزمون‌نشده:** REALITY/ECH، mKCP، HTTPUpgrade، h3، mux/XUDP، و رفتار طولانی‌مدت/reuse یک ترنسپورت | T1/T4 دقت ادعا | نه |
| R7 | **انجام شد — استفادهٔ واقعی و ماندگار:** نصب unit و `enable --now`، pool کاربری import شد، ۳۴ قاعده برای دامنه‌های کاربر، mode `targeted`، پروکسی دسکتاپ KDE روی RouteLatch با مسیر بازگشت. سه بررسی واقعی (approved→exit، unapproved→direct، exit مرده→502) pass. **شروع در login فقط config-verified**؛ reboot اجرا نشد | A11/A13 در استفادهٔ واقعی + تجربهٔ dogfood | انجام شد با مجوز شما |
| R8 | **انجام شد (`e098ceb` merge):** README/CI/فایل‌های عمومی، بستهٔ release با checksum/stamp/source offer، اسکن تاریخچه (پاک)، بررسی نام، `docs/release-readiness.md`. | آمادگی انتشار | انجام شد؛ push بعداً با مجوز کاربر انجام شد |
| R10 | **انجام شد (`c1a98f9`, `cbc6a87`, `3425c3f`):** failover CONNECT پیش از ۲۰۰ با سقف ۳ exit و بافر ۶۴ KiB، پین دستی تکـتلاش، بدون direct و بدون تغییر HTTP ساده/SOCKS5؛ ۱۶/۳۰ → ۹۲/۱۰۰ و smoke ۱۹/۲۰؛ نصب و restart سرویس روزمره | A02/A03/D22 | انجام شد |
| R11 | **باز — شروع نشده (درخواست کاربر ۲۰۲۶-۱۱-۱۸):** افزودن پشتیبانی واقعی از **hysteria2** و **sing-box** (Import + موتور + اثبات ترافیک). امروز `hysteria2://` به `unsupported`/`protocol_unsupported` و تنظیمات sing-box به `invalid` تبدیل میشود و هیچ تبدیلی به پروتکل پشتیبانی‌شده صورت نمیگیرد؛ پیگیری در R11. پیش از شروع، تصمیم معماری لازم است: گسترش موتور pin‌شده در برابر فرمت/رانتایم جداگانه، و مرز D-itemها (no MITM، hostname، fail-closed، quarantine). **بدون مجوز/تصمیم شروع نمیشود** | D07/A06/A08 (گسترش دامنه) | نیاز به تصمیم طراحی + مجوز |
| انتشار | **انجام شد (`3cb7bd7` هنگام push، HEAD فعلی `b01661d`):** بازنویسی تاریخچه در کپی کاری، push به forgejo و GitHub، تأیید عمومی بودن GitHub و برابری remote heads | A14 (انتشار) | مجوز کاربر |

**قاعدهٔ انتخاب:** R1–R8، R10 و انتشار بسته شدند؛ R9 (انتخاب کاربر) وجود ندارد و
تنها R11 باز است و بدون تصمیم معماری/مجوز شروع نمیشود.
کارهای باقیمانده (اجرای arm64، محیط پاک برای A01، reboot/re-login و GNOME) مجوز جدا
میخواهند؛ انتشار و push از قبل انجام شد، ولی tag/release مجوز جدا میخواهد.

### آنچه پذیرفته/اثبات نشده است

| معیار | وضعیت | دلیل/حد شاهد |
| --- | --- | --- |
| A02، A03، A05، A06، A08، A12 | اثبات محلی | شمارندهٔ تماس مقصد/خروجی و assertion مستقیم در harness؛ A06 شامل مشتق‌سازی legacy و مسیر import هر دو قاعده |
| WS/gRPC/XHTTP روی TLS | اثبات محلی (R6) | peer loopback pin‌شده، ۱ درخواست مقصد، pin غلط با ۰ داده؛ reuse طولانی‌مدت و h3 آزمون‌نشده |
| A04، A07، A09، A10 | اثبات محلی + نمونهٔ عمومی محدود (R4) | mode switch زنده+در جریان، rerank/cooldown، مجوز فایل/socket، CLI، launcher env، crash/restart، و ۴ exit عمومی با health/ranking واقعی؛ همهٔ مقاصد/اعضای pool نه |
| A13 | **اثبات واقعی (R1)** | نصب/فعال‌سازی unit واقعی و lifecycle؛ **اجرا نشد:** reboot/re-login و corrupt-state زیر systemd (offline فقط) |
| A01 | **پذیرفته‌نشده** | مسیر نصب واقعی و launch آن اثبات شد (R1)؛ ولی محیط پاک/ماشین تازه و اجرای arm64 هنوز نه |
| A11 | **اثبات واقعی روی KDE (R2)** | set/restore با diff OS، مالکیت، ویرایش کاربر، crash recovery؛ **اجرا نشد:** نشست واقعی GNOME |
| A14 | **مجوز/ممیزی/بسته بسته (R3+R8)** | Apache-2.0، ممیزی ۶۸ ماژول، بستهٔ release با checksum و source offer؛ طبق تصمیم کاربر بررسی رسمی نام لازم نیست، ایمیل committer قابلانتشار است، و push مجاز است — ./release-readiness.md |
| wire-level replay، UDP واقعی، arm64 اجرا | **قابل‌آزمون نیست** | خارج از loopback/مجوز؛ arm64 فقط cross-build شده |

**محدودیت‌های آزمون‌نشدهٔ دیگر که ادعا را محدود می‌کنند:** HTTP CONNECT exit، TLS-pin
failure بیرونی، REALITY/ECH، mKCP، HTTPUpgrade، XHTTP h3، mux/XUDP، reuse طولانی‌مدت
ترنسپورت، tick چرخش ۳۰ ثانیه‌ای (فقط در unit/code-path دیده شده) و رفتار با exit عمومی
کند/ناپایدار. اینها مجوز ضعیف‌کردن D-item نیستند.

### نقطهٔ شروع جلسهٔ بعد

1. `AGENTS.md` و همین بخش را بخوان؛ `git status` و `git log --oneline -5` را با
   «HEAD هنگام ثبت» مقایسه کن و تغییر/دادهٔ کاربر را حفظ کن.
2. R11 (پشتیبانی hysteria2/sing-box) با معماری «رانتایم دوم sing-box کنار Xray» و مجوز
   شروع کاربر، دروازهٔ مجوز/اسکیمای خارجی را پیش از کد میبندد. RouteLatch همین حالا نصب، فعال و به‌عنوان پروکسی دسکتاپ KDE تنظیم شده است (R7)؛ اگر کاربر خواست برگردد،
   `routelatch desktop proxy restore` مقدار قبلی را بازمی‌گرداند. کارهای بعدی — اجرای
   arm64 روی سخت‌افزار، ماشین تازه برای A01، reboot/re-login برای A13، نشست GNOME برای
   A11 و tag/release — مجوز جدا میخواهند؛ push به دو remote قبلاً انجام شد.
3. برای هر تغییر: آزمون واحد + به‌روزرسانی harness (جریان کامل بخشی از تعریف «انجام‌شده»
   است، چون F1/F8 را فقط آن گرفت)، سپس بازبینی مستقل و commit محلی.
4. شواهد و اعداد را از `spike/*/FINDINGS.md` و `docs/contracts.md` بردار، نه از حافظه.
   اگر دروازه‌ای شکست، تصمیم وابسته را باز کن و تضمین D-item را ضعیف نکن.
5. پایان هر task: ردیف وضعیت، HEAD، checks واقعی و قدم بعدی را همین‌جا به‌روز کن.

### مسائل و حدود مجوز

- **تصمیمهای کاربر ثبتشده (۲۰۲۶-۱۱-۱۸):** مسیر `/home/...` که در تاریخچهٔ `spike/` مانده
  با بازنویسی در یک کپی پاک میشود (تاریخچهای را که product نگه میدارد بازنویسی نمیکنیم)؛
  بررسی رسمی نام/علامت فعلاً لازم نیست و نام حفظ میشود؛ ایمیل committer قابلانتشار است؛
  و push مجاز است. شاهد کامل در `docs/release-readiness.md`.
- **remoteها:** `origin` = forgejo `routelatch.git`، و دومین remote = GitHub
  `m-hosseinpour/routelatch.git`؛ **فقط GitHub عمومی است**.
- **مجوز/ممیزی:** D20 با Apache-2.0 بسته شد (R3)؛ source offer و بستهٔ انتشار در R8 فراهم
  شدند. D22 (نقطهٔ تعهد CONNECT) با موافقت کاربر ثبت شد.
- **مجاز بدون درخواست تازه:** build/test محلی، دریافت وابستگی عمومی، commit محلی،
  تغییر کد/مستندات پروژه.
- **نیازمند درخواست جدا:** نصب/فعالسازی سرویس (برای R7 انجام شد)، تغییر proxy/DNS/تنظیمات
  دسکتاپ، استفاده از اطلاعات اتصال خصوصی، آزمون خروجیهای عمومی، tag/release، و
  دستکاری پروژهها/دادهٔ قبلی. push به دو remote فوق مجاز است.
