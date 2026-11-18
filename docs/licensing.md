# مجوز و ممیزی وابستگی‌ها

مرجع تصمیم مجوز (D20) و شاهد ممیزی وابستگی artifact — ۲۰۲۶-۱۱-۱۸

این سند مالک «چرا Apache-2.0»، روش ممیزی باینری، تعهدهای مجوزهای ثالث و
محدودیت‌های شاهد است. فهرست کامل ماژول‌ها، نسخه‌ها، مجوزها، copyright و متن
مجوزها در [`THIRD-PARTY-NOTICES.md`](../THIRD-PARTY-NOTICES.md) در ریشهٔ مخزن
است و همراه بسته منتشر می‌شود. متن مجوز خود پروژه در [`LICENSE`](../LICENSE) و
attribution پروژه در [`NOTICE`](../NOTICE) است. تصمیم کاربر در
[نیازمندی‌ها، D20](requirements.md) ثبت شده است.

## تصمیم

| مورد | مقدار |
| --- | --- |
| مجوز پروژه | **Apache-2.0** |
| دارندهٔ copyright | Mahdi Hosseinpour، سال ۲۰۲۶ |
| تاریخ تصمیم | ۲۰۲۶-۱۱-۱۸ |
| دامنهٔ ممیزی‌شده | باینری‌های لینوکس `amd64` و `arm64` (`CGO_ENABLED=0`) |
| مجوزهای ثالث | MIT، BSD-2/3، Apache-2.0، MPL-2.0، LGPL-3.0 (با استثنای لینک استاتیک) |
| مجوز ناشناخته | صفر |
| GPL/AGPL | هیچ |

Apache-2.0 انتخاب شد چون: با انتشار باینری و سورس سازگار است، اجازهٔ استفادهٔ
تجاری و تغییر می‌دهد، برای کتابخانه‌های Go رایج است، و تعهدهای انتساب آن با
`NOTICE`/`THIRD-PARTY-NOTICES.md` قابل اجراست. انتخاب مجوز، **مجوز انتشار
نیست**؛ push، remote، tag و release همچنان خارج از مجوز این کارند.

## روش ممیزی (چیزهایی که واقعاً اجرا شد)

ممیزی روی `go.mod` تنها نیست؛ مجموعهٔ ماژول‌هایی که واقعاً به باینری لینک
می‌شوند با toolchain pinشدهٔ `go1.27.4` بیرون کشیده شد:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go list -deps \
  -f '{{with .Module}}{{.Path}} {{.Version}}{{end}}' ./cmd/routelatch
# همان فرمان با GOARCH=arm64 و با GOOS=windows برای مقایسه
```

- **amd64 و arm64 دقیقاً همان ۶۸ ماژول ثالث را لینک می‌کنند.**
- `go version -m routelatch` روی باینری ساخته‌شده هم همان ۶۸ ماژول را نشان
  داد؛ یعنی فهرست `go list` با artifact واقعی می‌خواند، نه فقط گراف `go.mod`.
- `go mod verify` → `all modules verified`؛ محتوای ماژول‌های کش با `go.sum`
  مطابق است.
- هر ماژول به پوشهٔ `module@version` در module cache وصل شد و فایل‌های
  `LICENSE`/`COPYING`/`NOTICE`/`PATENTS` آن خوانده و دسته‌بندی شد. نتیجه در
  جدول `THIRD-PARTY-NOTICES.md` است.
- `go.mod` در کل ۷۳ requirement دارد. ۶۸ تا لینک می‌شوند؛ ۵ تا نمی‌شوند و در
  بخش «not linked» همان فایل با دلیل آمده‌اند: چهار ماژول فقط Windows
  (`coninput`، `go-localereader`، `wintun`، `wireguard/windows` — با
  `GOOS=windows go list` دیده شدند و در build لینوکس build-constrained‌اند) و
  `kr/text` که فقط از helper تست‌ها (`kr/pretty`) می‌آید و در باینری import
  نمی‌شود.

### توزیع مجوزها در ۶۸ ماژول

| SPDX | تعداد |
| --- | --- |
| MIT | ۳۶ |
| BSD-3-Clause | ۱۶ |
| Apache-2.0 | ۷ |
| MPL-2.0 | ۳ |
| BSD-2-Clause | ۲ |
| MIT AND Apache-2.0 | ۱ |
| BSD-3-Clause AND Apache-2.0 | ۱ |
| Apache-2.0 AND MIT | ۱ |
| LGPL-3.0-only WITH static-linking-exception | ۱ |

سه ماژول ترکیبی از دو مجوز دارند (فایل `LICENSE` آن‌ها بخش‌های متفاوت را جدا
اعلام می‌کند): `klauspost/compress` (BSD-3 برای ماژول و Apache-2.0 برای
`gzhttp/`)، `pelletier/go-toml` (MIT و Apache-2.0 برای `localtime.go`)، و
`gopkg.in/yaml.v2` (Apache-2.0 و MIT برای فایل‌های portشده از libyaml). متن هر
دو مجوز در notices آمده است.

## MPL-2.0: تعهد دقیق و بررسی در مخزن

سه ماژول MPL-2.0 در باینری هستند:

| ماژول | نسخه | hash در `go.sum` |
| --- | --- | --- |
| `github.com/xtls/xray-core` | `v1.260327.1-0.20261109052709-676eaf66f8f0` | `h1:p2uoadP+ulClbJ9dn+QioF2SfBqRly6VoKJosHqwcRo=` |
| `github.com/xtls/reality` | `v0.0.0-20261108101329-4bb46b2afd8b` | `h1:fAn11r21QiUY2oAGwENWb5ummjhWwMcs2WzkbjDMh+k=` |
| `layeh.com/gopher-luar` | `v1.0.11` | `h1:8zJudpKI6HWkoh9eyyNFaTM79PY6CAPcIr6X/KTiliw=` |

**بررسی شد، فرض نشد:** MPL کپی‌لفت در سطح فایل است، نه در سطح لینک. جز متن مجوز در اسناد notices، هیچ فایل سورس MPL و هیچ
`vendor/` در مخزن نیست (`git grep` و `git ls-files` بررسی شد)، هیچ فایل MPL تغییر نکرده، و موتور از طریق import بستهٔ عمومی Go
(`_ "github.com/xtls/xray-core/main/distro/all"` و API `core`) و به‌صورت
dependency بدون تغییر استفاده می‌شود. `go mod verify` و hashهای `go.sum` بالا
همین نسخهٔ بدون تغییر را pin می‌کنند.

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

محدودیت: این یک تحلیل از متن مجوز است، نه نظر حقوقی رسمی. اگر روزی این استثنا
از فایل مجوز حذف شود یا ماژول عوض شود، توزیع باینری باید بازبینی شود
(جایگزینی ماژول یا اجرای کامل §4 LGPL).

## چه چیزی بررسی نشد (ادعای فراتر از شاهد)

- فقط فایل‌های مجوز سطح ماژول خوانده شد؛ هدر فایل‌به‌فایل کل کد و قطعه‌کدهای
  ثالث داخل ماژول‌ها اسکن نشد. برای ماژول‌های combined، بخش‌های اعلام‌شده در
  `LICENSE` پوشش داده شد.
- ممیزی برای build لینوکس است. build ویندوز (آینده) ماژول‌های دیگری مثل
  `coninput`/`wintun` را لینک می‌کند و باید ممیزی جدا بگیرد.
- `arm64` فقط cross-build شده و اجرا نشده؛ مجموعهٔ ماژول‌ها با amd64 یکی است.
- بازبینی حقوقی مستقل انجام نشده؛ نام/علامت تجاری و ادعاهای امنیتی همچنان
  بخشی از T7 هستند.
- انتشار، source tarball، tag و release انجام نشده و مجاز نیست.

## تعهدهای باقی‌مانده پیش از انتشار (بخشی از A14)

1. **بسته‌بندی:** `LICENSE`، `NOTICE` و `THIRD-PARTY-NOTICES.md` باید داخل
   بستهٔ ساخته‌شده باشند. `scripts/package.sh` این‌ها را در
   `share/doc/routelatch/` می‌گذارد؛ بررسی ساخت بسته در همین تغییر انجام شد.
2. **Source offer:** برای MPL باید سورس نسخه‌های pinشده (و هر patch آینده)
   در دسترس باشد. دو راه قابل قبول: همراه‌کردن source tarball، یا offer
   نوشتاری معتبر. این تصمیم پیش از اولین انتشار لازم است.
3. **مجوز انتشار:** تصمیم D20 فقط مجوز پروژه را تعیین می‌کند؛ remote/push/tag/
   release و انتشار بسته نیاز به درخواست جدا دارد.
4. **بازبینی ادعای امنیت و نام:** بخش «ادعای امنیت» A14 و بررسی نام/علامت
   تجاری در T7 باقی است؛ این سند فقط مجوز و وابستگی را می‌بندد.

## جلوگیری از drift

`internal/licensing/notices_test.go` یک بررسی آفلاین و سریع است: وجود
`LICENSE`/`NOTICE`، پوشش کامل requirementهای `go.mod` در
`THIRD-PARTY-NOTICES.md` (با نسخهٔ یکسان)، و نبود مجوز خالی/`UNKNOWN`.
این تست `go list` را دوباره اجرا نمی‌کند تا به toolchain، module cache یا
شبکه وابسته نباشد؛ در عوض هر requirement در `go.mod` باید یا در جدول linked
باشد یا در جدول مستند «not linked» با دلیل. هزینهٔ این انتخاب: اگر `go.mod`
ماژولی را نگه دارد که هیچ‌وقت لینک نمی‌شود، باید دستی در جدول not linked
ثبت شود؛ و اگر ماژولی بدون تغییر `go.mod` به‌صورت ترانزیتی لینک شود ولی در
`go.mod` نیامده باشد، تست آن را نمی‌گیرد (Go 1.17+ همهٔ ماژول‌های لازم را در
`go.mod` می‌آورد، پس این حالت با `-mod=readonly` و `go mod tidy` بعید است).
ممیزی کامل با `go list -deps` و `go version -m` باید هنگام هر تغییر dependency
تکرار شود؛ این تست فقط جلوی drift خاموش را می‌گیرد.
