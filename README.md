# Go + Elasticsearch

API Gin untuk history pengajuan (`engine-detail`) dan latest state (`engine-summary`). Go berjalan lokal; Elasticsearch dan Kibana berjalan di Docker.

## Menjalankan

```powershell
docker compose up -d --wait
go run .
```

Memerlukan Go 1.26.1+ dan Docker. Elasticsearch/Kibana memakai versi 9.0.0. Kibana tersedia di http://localhost:5601 dan Elasticsearch di http://localhost:9200.

```powershell
$env:HTTP_ADDR = '127.0.0.1:8080'
$env:ELASTICSEARCH_URL = 'http://localhost:9200'
go run .
```

Nama alias tetap `engine-detail` dan `engine-summary`; konfigurasi lama `ELASTICSEARCH_INDEX`/`ELASTICSEARCH_SUMMARY_INDEX` tidak dipakai lagi. `.env.example` mengatur image Docker Compose.

## Struktur

- `main.go`: merangkai dependency dan menjalankan server serta worker rollover.
- `routes.go`: pendaftaran endpoint dan middleware.
- `handler.go`, `service.go`, `repo.go`: modul engine-detail.
- `engine_summary_handler.go`, `engine_summary_service.go`, `engine_summary_repo.go`: modul engine-summary.
- `elastic_indexes.go`: mapping bersama, template, alias, penomoran versi, dan rollover.
- `dummy.go`: handler/service/repo generator dummy, bulk 500 dokumen dan pemeriksaan setiap item.
- `export/handler.go`, `export/service.go`, `export/repo.go`: streaming NDJSON dari summary ke detail melalui PIT dan search_after.
- `utils_response.go`, `utils_pagination.go`: response dan pagination.
- `elastic-query.md`: query setup, alias, rollover, count, pencarian dan aturan write.

Alur request: `routes -> handler -> service -> repo -> Elasticsearch`.

## Index dan alias

| Alias | Concrete index setelah dummy | Root documents | Write |
| --- | --- | ---: | --- |
| engine-detail | engine-detail-0001 | 30.000 | false |
| engine-detail | engine-detail-0002 | 30.000 | false |
| engine-detail | engine-detail-0003 | 20.000 | true |
| engine-summary | engine-summary-v001 | 20.000 | true |

Search lewat alias membaca semua versi. Write dokumen baru melalui alias menuju versi paling baru. Startup mendaftarkan template, membuat versi awal jika belum ada, dan memasang alias ke versi yang ditemukan. Legacy concrete index bernama sama dengan alias harus dimigrasikan/dihapus dahulu; startup tidak menghapus data otomatis.

Saat Go berjalan, worker memeriksa setiap 30 detik dan melakukan rollover jika write index sudah mencapai 30.000 dokumen utama. Penamaan berikutnya adalah `engine-detail-0004`, dst. dan `engine-summary-v002`, dst. Writer dummy membatasi setiap batch sesuai kapasitas agar versi tidak melebihi 30.000. Write dari proses luar dapat melewati ambang sebelum worker berikutnya berjalan. Jalankan satu instance Go sebagai pemilik lifecycle.

Gunakan `_count`, bukan `_cat/indices docs.count`, untuk menghitung row detail karena `variables` bertipe nested.

## Field

| Field | Tipe Elasticsearch | Keterangan |
| --- | --- | --- |
| applicationID | keyword | Boleh berulang di detail, satu latest state per aplikasi pada summary |
| name | text | Nama pemohon |
| debt | double | Nominal pinjaman |
| address | text | Alamat pemohon |
| decision_result | keyword | reject atau approve |
| created_at | date | Timestamp RFC3339 |
| variables | nested | Khusus detail: array object; age integer, married boolean, salary double |

Key tambahan pada variables didukung dynamic mapping. Dokumen tanpa variables ditampilkan dengan `variables: []`.
Response menyertakan `id` dari `_id` Elasticsearch. Mapping keyword tidak memvalidasi enum decision_result; proses ingest bertanggung jawab memvalidasi nilainya.

## Endpoint

| Method | Path | Fungsi |
| --- | --- | --- |
| GET | `/health` | Status koneksi Elasticsearch |
| GET | `/export?name=Budi&decision_result=approve` | Export detail latest-state sebagai stream NDJSON; filter per field summary |
| GET | `/engine-detail/{applicationID}` | Semua history yang cocok persis, lintas versi, maksimal 100 hasil |
| GET | `/engine-summary?page=1&size=10` | Pagination latest state lintas versi |
| GET | `/engine-summary?q=budi&applicationID=APP-000001` | Filter nama/alamat dan applicationID |
| GET | `/engine-summary/{id}` | Summary berdasarkan `_id`, melalui search alias lintas versi |
| POST | `/dummy` | Reset keluarga index dan isi ulang dummy |

Summary size default 10, maksimum 100; page mulai 1. `limit` masih diterima sebagai alias size. Query memakai from/size, exact total, dan urutan applicationID; halaman di atas 10.000 hasil memakai search_after bertahap. Halaman jauh memerlukan beberapa query. Hindari regenerasi data saat berpindah halaman.

Response list summary:

```json
{
  "status": 200,
  "data": [],
  "meta": {
    "page": 1,
    "size": 10,
    "total_records": 0,
    "total_pages": 0,
    "has_next": false,
    "has_previous": false
  }
}
```

`data` berisi dokumen halaman tersebut. Endpoint tanpa pagination hanya memiliki status/data. Error memakai `data.message`.

## Dummy

```powershell
Invoke-RestMethod http://localhost:8080/dummy -Method Post -TimeoutSec 330
```

**Endpoint ini menghapus seluruh concrete index lama/versi milik engine-detail dan engine-summary, lalu membuat ulang dataset.** Gunakan hanya untuk data dummy. Mapping baru mengikuti template dalam kode.

Hasil: 20.000 aplikasi APP-000001 sampai APP-020000; masing-masing 4 history sehingga total 80.000 detail. created_at naik satu detik per row dalam empat putaran aplikasi. Rentang tanggal detail-0001 paling lama, detail-0002 setelahnya, dan detail-0003 paling baru. Nilai nominal, keputusan dan salary diacak, tetapi penempatan versi/tanggal tidak diacak.

Summary berisi tepat 20.000 dokumen, satu per applicationID, menyalin semua field bersama dari history terakhir. Variables tetap detail-only. `_id` detail memakai APP-000001-01 hingga -04; summary memakai APP-000001.

Semua bulk detail harus berhasil sebelum summary ditulis. Response memuat jumlah total, jumlah per versi, dan write index aktif. Setelah selesai, index di-refresh dan count diperiksa. Request sinkron memiliki timeout 5 menit. Request dummy bersamaan pada proses yang sama mendapat HTTP 409. Kegagalan dapat meninggalkan dataset parsial; ulangi POST /dummy untuk reset dan mengisi ulang.

Summary tidak otomatis mengikuti insert manual ke detail. Untuk update summary setelah rollover, cari applicationID dan update concrete index tempat dokumen lama berada agar tidak membuat duplikat di versi terbaru. Lihat [elastic-query.md](elastic-query.md).

## Export NDJSON

```powershell
curl.exe --no-buffer 'http://localhost:8080/export' --output engine-export.ndjson
curl.exe --no-buffer 'http://localhost:8080/export?applicationID=APP-000001'
curl.exe --no-buffer --get 'http://localhost:8080/export' --data-urlencode 'name=Budi Santoso' --data-urlencode 'address=Jakarta' --data-urlencode 'decision_result=approve' --output engine-export.ndjson
```

Endpoint ini khusus streaming: content type `application/x-ndjson`, satu object detail per baris tanpa envelope status/data/meta. Tidak menerima pagination page/size: seluruh hasil sesuai filter diekspor. Hasil kosong berupa HTTP 200 dengan body kosong.

Filter export dikirim satu per field sebagai query parameter. Semuanya opsional dan digabung dengan AND; tanpa filter, seluruh summary diekspor. Tidak ada lagi parameter pencarian gabungan `q` atau `query-filter`.

| Parameter | Perilaku |
| --- | --- |
| `id` | Exact `_id` dokumen summary |
| `applicationID` | Exact keyword |
| `name` | Match phrase khusus field name (mengikuti analyzer text) |
| `debt` | Nominal exact, angka non-negatif; `0` tetap diterapkan sebagai filter |
| `address` | Match phrase khusus field address (mengikuti analyzer text) |
| `decision_result` | Exact `approve` atau `reject` |
| `created_at` | Timestamp exact sesuai mapping date; RFC3339, misalnya `2026-09-28T02:24:04Z` |

Parameter tidak dikenal, nilai kosong/berulang, angka tidak valid, atau format timestamp salah mengembalikan HTTP 400 sebelum PIT/stream dimulai. Gunakan URL encoding untuk spasi dan tanda `+` pada timezone; contoh curl menggunakan `--data-urlencode`.

Alur: buka satu PIT pada `engine-summary,engine-detail` (keep_alive 2m). Query summary memakai filter `_index: engine-summary`, mengambil 500 summary dengan sort `created_at desc`, `applicationID asc`. Query detail memakai filter `_index: engine-detail` dan pasangan applicationID + created_at yang cocok persis. Matching menggunakan satu query per batch, bukan request per row. Data detail termasuk variables dan id ditulis sesuai urutan summary, bukan urutan hasil lookup detail.

Seluruh array sort hit terakhir summary diteruskan apa adanya sebagai search_after, termasuk implicit _shard_doc. Summary dan detail berbagi satu PIT ID; jika salah satu query mengembalikan ID baru, query berikutnya dan cleanup memakai ID tersebut. Keep_alive diperbarui pada setiap search. Query PIT tidak memakai `.Index()`; pemilihan sumber dilakukan lewat filter `_index`, yang mendukung nama alias. PIT bukan transaksi write lintas index; jangan menjalankan reset dummy atau mengubah anggota alias secara manual selama export berlangsung.

Memori hanya menyimpan batch aktif dan buffer HTTP 64 KB. Setelah batch ditulis, buffer dan HTTP response di-flush sebelum query berikutnya. Client lambat menahan batch berikutnya (backpressure). Timeout export 30 menit, dengan batas network write 30 detik per batch; endpoint biasa tetap memiliki timeout sebelumnya.

Jika client disconnect atau context dibatalkan, query dihentikan dan satu PIT bersama tetap ditutup memakai context cleanup terpisah dengan timeout 10 detik. Kegagalan cleanup dicatat di log dan PIT memiliki expiry 2 menit sebagai cadangan.

Jika pasangan detail hilang/duplikat atau Elasticsearch mengembalikan partial result, export dihentikan. Sebelum stream dimulai, error memakai response API biasa. Sesudah stream dimulai, error hanya dicatat dan koneksi stream diselesaikan tanpa menambahkan JSON error ke baris data. Karena status HTTP sudah terkirim, frontend harus memperlakukan hasil yang terputus sebagai export parsial; tidak ada error envelope di tengah NDJSON.

Referensi: [PIT dan search_after Elasticsearch](https://www.elastic.co/docs/reference/elasticsearch/rest-apis/paginate-search-results).

## Kibana

Buat data view `engine-detail` dan `engine-summary` menggunakan nama alias, dengan time field created_at. Sesuaikan rentang waktu untuk melihat data. Index Management menampilkan versi konkret; Dev Tools bisa menjalankan query dalam elastic-query.md.
