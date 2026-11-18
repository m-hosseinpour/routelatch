# مجوز و ممیزی وابستگی‌ها

مرجع تصمیم مجوز (D20) و شاهد ممیزی وابستگی artifact — ۲۰۲۶-۱۱-۱۸

این سند مالک «چرا Apache-2.0»، روش ممیزی باینری، تعهدهای مجوزهای ثالث و
محدودیت‌های شاهد است. فهرست کامل ماژول‌ها، کامپوننت‌های embedشده، نسخه‌ها،
مجوزها، copyright و متن مجوزها در [`THIRD-PARTY-NOTICES.md`](../THIRD-PARTY-NOTICES.md)
در ریشهٔ مخزن است و همراه بسته منتشر می‌شود. متن مجوز خود پروژه در
[`LICENSE`](../LICENSE) و attribution پروژه در [`NOTICE`](../NOTICE) است. تصمیم
کاربر در [نیازمندی‌ها، D20](requirements.md) ثبت شده است.

## تصمیم

| مورد | مقدار |
| --- | --- |
| مجوز پروژه | **Apache-2.0** |
| دارندهٔ copyright | Mahdi Hosseinpour، سال ۲۰۲۶ |
| تاریخ تصمیم | ۲۰۲۶-۱۱-۱۸ |
| دامنهٔ ممیزی‌شده | باینری‌های لینوکس `amd64` و `arm64` (`CGO_ENABLED=0`) |
| ماژول ثالث لینک‌شده | ۶۸ |
| کامپوننت embedشده با مجوز مستقل | ۶ |
| مجوزهای ثالث | MIT، BSD-2/3، Apache-2.0، MPL-2.0، LGPL-3.0 (با استثنای لینک استاتیک) |
| مجوز ناشناخته | صفر |
| GPL/AGPL به‌عنوان مجوز ماژول | هیچ |

«هیچ GPL/AGPL» در جدول بالا مربوط به **همان ۶۸ ماژول لینکشده به باینری
RouteLatch** است. باینری دوم و جداگانهٔ GPL-3.0 (sing-box) که در یک بستهٔ release
همراه میشود، وارد `go.mod` نمیشود و در این ممیزی حساب نمیشود؛ بخش جداگانهٔ
«رانتایم دوم جداگانهٔ GPL-3.0» در پایین همین سند آن را پوشش میدهد.

Apache-2.0 انتخاب شد چون: با انتشار باینری و سورس سازگار است، اجازهٔ استفادهٔ
تجاری و تغییر می‌دهد، برای کتابخانه‌های Go رایج است، و تعهدهای انتساب آن با
`NOTICE`/`THIRD-PARTY-NOTICES.md` قابل اجراست. انتخاب مجوز، **مجوز انتشار
نیست**؛ push، remote، tag و release همچنان خارج از مجوز این کارند.

## روش ممیزی (چیزهایی که واقعاً اجرا شد)

ممیزی روی `go.mod` تنها نیست؛ مجموعهٔ ماژول‌ها **و پکیج‌های**ی که واقعاً به
باینری لینک می‌شوند با toolchain pinشدهٔ `go1.27.4` بیرون کشیده شد:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go list -deps \
  -f '{{with .Module}}{{.Path}} {{.Version}}{{end}}' ./cmd/routelatch
# همان فرمان با GOARCH=arm64 و با GOOS=windows برای مقایسه
go list -deps -f '{{if .Module}}{{.Module.Path}}|{{.Module.Version}}|{{.Dir}}{{end}}' \
  ./cmd/routelatch   # پکیج‌ها برای یافتن مجوزهای داخل پکیج
```

- **amd64 و arm64 دقیقاً همان ۶۸ ماژول ثالث را لینک می‌کنند.**
- `go version -m routelatch` روی باینری ساخته‌شده هم همان ۶۸ ماژول را نشان
  داد؛ یعنی فهرست `go list` با artifact واقعی می‌خواند، نه فقط گراف `go.mod`.
- `go mod verify` → `all modules verified`؛ محتوای ماژول‌های کش با `go.sum`
  مطابق است. `go.mod` هیچ `replace` ندارد و `vendor/` وجود ندارد.
- هر ماژول به پوشهٔ `module@version` در module cache وصل شد و فایل‌های
  `LICENSE`/`COPYING`/`NOTICE`/`PATENTS` سطح ماژول خوانده و دسته‌بندی شد.
- **مجوزهای داخل پکیج‌ها هم اسکن شد:** برای هر پکیج لینک‌شده، فایل‌های مجوز
  همان پوشه خوانده شد. این کار ۶ کامپوننت با مجوز مستقل پیدا کرد که با اتکا به
  `LICENSE` ریشهٔ ماژول دیده نمی‌شدند: `internal/snapref` (BSD-3، Snappy-Go
  Authors)، `zstd/internal/xxhash` (MIT، Caleb Spare)، `dicttls` (BSD-3،
  Gaukas Wang)، `LICENSE-Go` در reality (BSD-3، Go Authors)،
  `transport/internet/masque/connectip` (MIT، Marten Seemann) و
  `LICENSE.libyaml` (MIT، Kirill Simonov). این‌ها در جدول
  «Embedded third-party components» و در بخش copyright همان فایل آمده‌اند.
- `go.mod` در کل ۷۳ requirement دارد. ۶۸ تا لینک می‌شوند؛ ۵ تا نمی‌شوند و در
  بخش «not linked» همان فایل با دلیل آمده‌اند: چهار ماژول فقط Windows
  (`coninput`، `go-localereader`، `wintun`، `wireguard/windows` — با
  `GOOS=windows go list` دیده شدند و در build لینوکس build-constrained‌اند) و
  `kr/text` که از مسیر تست‌های `yaml.v2` → `kr/pretty` می‌آید و در باینری import
  نمی‌شود.

### توزیع مجوزها در ۶۸ ماژول

| SPDX | تعداد |
| --- | --- |
| MIT | ۳۵ |
| BSD-3-Clause | ۱۶ |
| Apache-2.0 | ۷ |
| BSD-2-Clause | ۲ |
| MPL-2.0 | ۱ |
| MIT AND Apache-2.0 | ۱ |
| Apache-2.0 AND MIT | ۱ |
| MIT AND BSD-3-Clause | ۱ |
| BSD-3-Clause AND MIT | ۱ |
| MPL-2.0 AND BSD-3-Clause | ۱ |
| MPL-2.0 AND MIT | ۱ |
| LGPL-3.0-only WITH LicenseRef-static-linking-exception | ۱ |

شش ماژول بیش از یک مجوز دارند و فایل `LICENSE` یا کامپوننت داخلی آن‌ها بخش‌ها
را جدا اعلام می‌کند: `pelletier/go-toml` (MIT + Apache-2.0 برای `localtime.go`)،
`gopkg.in/yaml.v2` (Apache-2.0 + MIT برای فایل‌های libyaml)، `klauspost/compress`
(BSD-3 + MIT؛ بخش Apache-2.0 همان فایل فقط برای `gzhttp/` است که لینک نمی‌شود)،
`ghodss/yaml` (MIT + بخش BSD-3 برای کد Go)، `xtls/reality` (MPL-2.0 +
`LICENSE-Go` با BSD-3) و `xtls/xray-core` (MPL-2.0 + `connectip` با MIT). متن
هر دو/هر سه مجوز در notices آمده است.

## MPL-2.0: تعهد دقیق و بررسی در مخزن

سه ماژول MPL-2.0 در باینری هستند:

| ماژول | نسخه | hash در `go.sum` |
| --- | --- | --- |
| `github.com/xtls/xray-core` | `v1.260327.1-0.20261109052709-676eaf66f8f0` | `h1:p2uoadP+ulClbJ9dn+QioF2SfBqRly6VoKJosHqwcRo=` |
| `github.com/xtls/reality` | `v0.0.0-20261108101329-4bb46b2afd8b` | `h1:fAn11r21QiUY2oAGwENWb5ummjhWwMcs2WzkbjDMh+k=` |
| `layeh.com/gopher-luar` | `v1.0.11` | `h1:8zJudpKI6HWkoh9eyyNFaTM79PY6CAPcIr6X/KTiliw=` |

**بررسی شد، فرض نشد:** MPL کپی‌لفت در سطح فایل است، نه در سطح لینک. جز متن مجوز
در اسناد notices، هیچ فایل سورس MPL و هیچ `vendor/` در مخزن نیست (`git grep` و
`git ls-files` بررسی شد)، هیچ فایل MPL تغییر نکرده، و موتور از طریق import بستهٔ
عمومی Go (`_ "github.com/xtls/xray-core/main/distro/all"` و API `core`) و
به‌صورت dependency بدون تغییر استفاده می‌شود. `go mod verify` و hashهای
`go.sum` بالا همین نسخهٔ بدون تغییر را pin می‌کنند. پکیج `connectip` داخل
xray-core مجوز MIT جدا دارد و در notices انتساب شده است؛ این مجوز جدا، MPL
فایل‌های دیگر را تغییر نمی‌دهد.

تعهد §3.2 MPL برای توزیع Executable Form:

1. متن مجوز MPL-2.0 همراه توزیع باشد — در `THIRD-PARTY-NOTICES.md` هست.
2. به دریافت‌کننده گفته شود سورس Covered Software را چگونه به دست آورد —
   در `NOTICE`، با دستور `go mod download <module>@<version>` و نسخهٔ دقیق.
3. اگر فایل MPL تغییر کند، سورس همان فایل باید MPL و در دسترس باشد. امروز
   تغییری نیست؛ اگر در آینده patch شود، source offer باید آن patch را هم شامل
   شود.
4. کد خود RouteLatch «Larger Work» است و Apache-2.0 می‌ماند؛ MPL کد ما را
   MPL نمی‌کند.

## LGPL-3.0: یافتهٔ کپی‌لفت و مبنای سازگاری

`github.com/juju/ratelimit v1.0.2` (از مسیر
`internal/contract` → `xray-core/main/distro/all` → `transport/internet/grpc` →
`xtls/reality` → `juju/ratelimit`) با LGPL-3.0 وارد باینری می‌شود. این یک
وابستگی ترانزیتی ناخواسته است و باید صریح ثبت شود.

فایل `LICENSE` همین ماژول، علاوه بر LGPLv3، یک **استثنای صریح لینک استاتیک**
از صاحبان copyright (Canonical) دارد: انتقال Combined Work که به‌صورت استاتیک
به کتابخانه لینک می‌شود، بدون ارائهٔ Minimal Corresponding Source یا
installation information مجاز است، مشروط به رعایت بقیهٔ مفاد LGPL3 و شرایط
مجوز Application (اینجا Apache-2.0). متن کامل، شامل استثنا، در
`THIRD-PARTY-NOTICES.md` بازتولید شده است. بنابراین انتشار باینری استاتیک
Apache-2.0 با این ماژول، بر پایهٔ متن همان فایل مجوز، سازگار است — نه بر پایهٔ
فرض.

استثنا فقط بخش‌های 4(d) و 4(e) را کنار می‌گذارد. بخش 4(b) LGPL-3.0 همچنان یک
نسخه از GNU GPL را می‌خواهد؛ متن کامل **GPL-3.0** در notices (بخش
«GPL-3.0 (incorporated by LGPL-3.0)») آمده و این تعهد را پوشش می‌دهد. تحلیل
حقوقی رسمی انجام نشده است؛ اگر روزی این استثنا از فایل مجوز حذف شود یا ماژول
عوض شود، توزیع باینری باید بازبینی شود (جایگزینی ماژول یا اجرای کامل §4 LGPL).

## رانتایم دوم جداگانهٔ GPL-3.0 (sing-box) — ارزیابی ریسک حقوقی

این بخش مربوط به R11 و تصمیم ثبتشده در
[پلن، بخش کارهای باز](implementation-plan.md#open-work) است: sing-box `v1.14.4`
به عنوان **executable جداگانه و تغییرناپذیر** کنار باینری Apache-2.0 RouteLatch
ship میشود. RouteLatch با آن از طریق inbound محلی و socket بهصورت arm's-length
صحبت میکند؛ این دو برنامه یک «aggregate» تحت بند آخر GPL-3.0 §5 (و §0)اند، نه
یک اثر ترکیبی واحد. **مجوز باینری اصلی RouteLatch و ممیزی ۶۸ ماژول تغییری
نمیکنند**: `LICENSE`، `NOTICE` و جدول ممیزی بالا مربوط به همان باینری Apache-2.0
اند و sing-box وارد `go.mod` نمیشود.

| مورد | مقدار |
| --- | --- |
| باینری دوم | `bin/sing-box` — executable مستقل، لینکنشده به RouteLatch |
| نسخهٔ pinشده | tag `v1.14.4` (۲۰۲۶-۱۰-۲۴)، commit `c005f35300ce76427dc285481ec5695f5b179cd2` |
| مجوز | **GPL-3.0-or-later** به علاوهٔ یک بند اضافهٔ نامگذاری؛ SPDX گیتهاپ `NOASSERTION` |
| کتابخانهٔ base | `SagerNet/sing` با مجوز GPL-3.0-or-later؛ سورس آن بخشی از Corresponding Source است |
| تغییرات | هیچ؛ باینری upstream دقیقاً همانطور ship میشود |
| هستهٔ hysteria2 | `github.com/apernet/hysteria/core/v2` در tag `core/v2.14.0` با مجوز MIT — وابستگی سورس sing-box است، **نه** وابستگی Goی RouteLatch |

**بند نامگذاری.** متن دقیق `LICENSE` upstream (عیناً در
`THIRD-PARTY-NOTICES.md` بازتولید شده است) در پاراگراف آخر میگوید:

> "In addition, no derivative work may use the name or imply association
> with this application without prior consent"

به همین دلیل گیتهاپ SPDX سادهٔ `GPL-3.0-or-later` گزارش نمیکند. رعایت آن بدین
صورت است: باینری upstream بدون تغییر ship میشود، فقط در محل افشای وجود آن با نام
واقعی `sing-box` معرفی میشود، هیچ build تغییریافتهٔای به این نام عرضه نمیشود و
هیچ ارتباط یا تأیید ضمنی ادعا نمیشود. نام و برند RouteLatch مستقل است.

**تعهد source offer (GPL-3.0 §6).** هنگامی که باینری بدون سورسش ارسال میشود، باید
پیشنهاد نوشتاری سورس همراهش باشد. متن کامل GPL-3.0 از قبل در
`THIRD-PARTY-NOTICES.md` آمده و همان فایل حاوی پیشنهاد نوشتاری معتبر حداقل سه سال
و به مدت ارائهٔ باینری است. دستور دقیق سورس نسخهٔ pinشده:

```sh
git clone https://github.com/SagerNet/sing-box.git sing-box-v1.14.4
cd sing-box-v1.14.4
git checkout c005f35300ce76427dc285481ec5695f5b179cd2   # tag v1.14.4
```

این درخت `SagerNet/sing` و هستهٔ hysteria2 را در `go.mod`/`go.sum` خود pin
میکند؛ دریافت همان نسخهها Corresponding Source را کامل میکند. مشخصات باینری و
checksum در `SINGBOX-INFO` داخل بسته و `dist/singbox/SHA256SUMS` هنگام ساخت ثبت
میشود. جزئیات چگونگی تهیه و تأیید باینری در `packaging/README.md` است و بسته شدن
دروازهٔ منبع رسمی پیش از انتشار وظیفهٔ اپراتور release است.

**ماهیت این ارزیابی.** این یک ارزیابی ریسک حقوقی داخلی پروژه است، **نظر مشورتی
حقوقی نیست** و توسط وکیل بازبینی نشده است. متن دقیق `LICENSE` upstream شامل بند
نامگذاری و اعلان copyright از فایل خام رسمی در tag `v1.14.4` و commit pinشده
(دو نسخه یکسان) گرفته و عیناً در `THIRD-PARTY-NOTICES.md` بازتولید شده است.
همچنین checksum معتبر آرشیوهای رسمی release (`sing-box-1.14.4-linux-amd64.tar.gz`
و `sing-box-1.14.4-linux-arm64.tar.gz`) از GitHub API گرفته و در
`scripts/package.sh` pin شدهاند (راهنمای تأیید در `packaging/README.md`). موردی که
همچنان تأییدنشده باقی میماند **باینری/checksum نهایی تأمینشده توسط اپراتور** است:
اپراتور باید آرشیو را با checksum pinشده تأیید، باینری را از همان آرشیو استخراج و
سپس `SHA256SUMS` بسازد. اسکریپت provenance را تنها وقتی میتواند به منبع رسمی متصل
کند که آرشیو هم supply شده باشد؛ بدون آرشیو فقط باینری اپراتور بررسی میشود (در
build معمولی مجاز، در release با `REQUIRE_SINGBOX=1` خطا). از آنجا که باینری به
عنوان برنامهٔ جداگانه ship میشود و نه جاسازیشده در RouteLatch، تعهد کپیلفت GPL به
باینری RouteLatch سرایت نمیکند؛ اگر روزی جاسازی (embedding) در نظر گرفته شود، تمام
این تحلیل و D20/Apache-2.0 باید باز شود و مجوز جدا لازم است.

## چه چیزی بررسی نشد (ادعای فراتر از شاهد)

- فایل‌های مجوز سطح ماژول و سطح پکیج‌های لینک‌شده خوانده شد و copyrightهای داخل
  خود فایل‌های مجوز، شامل بلوک انتهای فایل‌های Apache-2.0، استخراج شد.
  **هدر فایل‌به‌فایل همهٔ سورس‌ها برای copyright‌های فردی اسکن نشد.** مثلاً `Psiphon Inc.` در
  `utls/u_prng.go`، `Ross Oreto` در `quic-go/internal/utils/tree`، `Arm Limited`
  در `go-brrr/internal/encoder/log2.go` و `Google LLC` در `go-toml/localtime.go`
  فقط در هدر فایل‌اند و در فهرست bullets نیامده‌اند؛ مجوز و متن آن‌ها پوشش داده
  شده، ولی این نام‌ها به‌صورت جدا انتساب نشده‌اند.
- runtime و stdlib خود Go (BSD-3، The Go Authors) داخل باینری است؛ در notices
  به‌عنوان BSD-3 و Copyright 2009 The Go Authors ذکر شده، اما فهرست فایل‌های
  stdlib جداگانه تولید نشده است.
- `golang.org/x/crypto/x509roots/fallback` دادهٔ گواهی ریشهٔ مشتق از Mozilla را
  embed می‌کند؛ وضعیت مجوز خود آن داده جدا تحلیل نشده است.
- ممیزی برای build لینوکس است. build ویندوز (آینده) ماژول‌های دیگری مثل
  `coninput`/`wintun` را لینک می‌کند و باید ممیزی جدا بگیرد.
- `arm64` فقط cross-build شده و اجرا نشده؛ مجموعهٔ ماژول‌ها با amd64 یکی است.
- بازبینی حقوقی مستقل انجام نشده؛ نام/علامت تجاری و ادعاهای امنیتی همچنان
  بخشی از T7 هستند.
- انتشار، source tarball، tag و release انجام نشده و مجاز نیست.

## تعهدهای باقی‌مانده پیش از انتشار (بخشی از A14)

1. **بسته‌بندی:** `LICENSE`، `NOTICE`، `THIRD-PARTY-NOTICES.md` و همین سند
   (`docs/licensing.md`) به‌همراه `go.sum` داخل بسته می‌روند؛
   `scripts/package.sh` این کار را می‌کند و ساخت بسته در همین تغییر بررسی شد.
   `SERVICE.md` (نسخهٔ بستهٔ `deploy/README.md`) هم اضافه شد تا ارجاع
   `INSTALL.md` به uninstall و crash recovery داخل بسته resolve شود.
   `package.sh` stamp باینری معماری host (version/commit/date) را با
   `BUILD-INFO` مقایسه می‌کند و در
   اختلاف fail می‌کند؛ پس بستهٔ با stamp ناهم‌خوان ساخته نمی‌شود.
2. **Source offer:** برای MPL باید سورس نسخه‌های pin‌شده (و هر patch آینده)
   در دسترس باشد. بستهٔ فعلی راه **offer نوشتاری** را پیاده می‌کند: `NOTICE`
   (در `share/doc/routelatch/NOTICE`) نسخهٔ دقیق سه ماژول MPL، دستور
   `go mod download` و مخزن upstream را می‌دهد. تا وقتی این فایل همراه بسته
   منتشر شود، دریافت‌کننده مسیر مشخصی به سورس Covered Software دارد.
   همراه‌کردن source tarball یک جایگزین است، نه الزام؛ اگر مالک پروژه آن را
   ترجیح دهد، `scripts/package.sh` باید گسترش یابد.
3. **مجوز انتشار:** تصمیم D20 فقط مجوز پروژه را تعیین می‌کند؛ remote/push/tag/
   release و انتشار بسته نیاز به درخواست جدا دارد.
4. **بازبینی ادعای امنیت و نام:** بخش «ادعای امنیت» A14 و بررسی نام/علامت
   تجاری در T7 باقی است.

## جلوگیری از drift

`internal/licensing/notices_test.go` یک بررسی آفلاین و سریع است:

- وجود `LICENSE` و `NOTICE` و متن canonical؛
- پوشش کامل requirementهای `go.mod` در `THIRD-PARTY-NOTICES.md` با نسخهٔ
  یکسان، و رد هر `replace` (چون ماژول جایگزین ممیزی نشده است);
- هم‌خوانی شمارش هر خانوادهٔ مجوز در summary با جدول، و مجموع با total؛
- وجود حداقل یک bullet انتساب/copyright برای هر ماژول لینک‌شده.

این تست `go list` را دوباره اجرا نمی‌کند تا به toolchain، module cache یا
شبکه وابسته نباشد؛ در عوض هر requirement در `go.mod` باید یا در جدول linked
باشد یا در جدول مستند «not linked» با دلیل. **محدودیت‌های باقی‌مانده:** تست
نمی‌تواند تشخیص دهد که یک ماژول واقعاً لینک می‌شود یا نه (یک ردیف می‌تواند با
دلیل ساختگی به جدول not linked منتقل شود)، و اگر کسی عمداً مجوز یک ماژول را
به یک مجوز شناخته‌شدهٔ دیگر عوض کند، تست سبز می‌ماند. به همین دلیل ممیزی کامل
با `go list -deps`، اسکن مجوز پکیج‌ها و `go version -m` هنگام هر تغییر
dependency اجباری است؛ این تست فقط جلوی drift خاموش را می‌گیرد.
