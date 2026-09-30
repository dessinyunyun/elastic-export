# Elasticsearch: index version, alias, dan rollover

Jalankan query berikut di Kibana > Dev Tools (http://localhost:5601).

## Hasil dataset dummy

| Alias baca/tulis | Concrete index | Dokumen utama | Write |
| --- | --- | ---: | --- |
| engine-detail | engine-detail-0001 | 30.000 | false |
| engine-detail | engine-detail-0002 | 30.000 | false |
| engine-detail | engine-detail-0003 | 20.000 | true |
| engine-summary | engine-summary-v001 | 20.000 | true |

`0001` memiliki tanggal paling lama, `0002` lebih baru, `0003` paling baru.
Ada 20.000 applicationID, masing-masing 4 history. Summary menyalin history terakhir per aplikasi; seluruh history terakhir berada di detail-0003.
`variables` hanya ada pada detail. Summary menyimpan semua field bersama yang sama dengan history terbaru.

## 1. Reset index lama (destruktif, hanya untuk mengganti dataset dummy)

Nama alias tidak boleh sama dengan concrete index yang sudah ada. Hapus concrete index lama satu kali sebelum menjalankan aplikasi versi alias.
Jangan jalankan DELETE ini setelah nama tersebut menjadi alias; jika sudah bermigrasi, langsung gunakan POST /dummy untuk reset dataset.

```http
DELETE /engine-detail
DELETE /engine-summary
```

`POST http://localhost:8080/dummy` menghapus index version yang cocok persis dengan pola yang dikelola aplikasi, lalu membuat ulang dan mengisi dataset di atas. Semua data lama dalam keluarga index tersebut akan hilang.
Aplikasi tidak menghapus data saat startup biasa. Jika legacy concrete index masih ada, startup berhenti dengan pesan migrasi.

## 2. Template mapping dan setting untuk semua versi baru

Template tidak menetapkan write alias, agar tidak muncul dua write index.
Aplikasi mengelola alias dengan `_aliases` dan `_rollover`.
Setting satu shard dan nol replica digunakan untuk Docker lokal single-node.

```http
PUT /_index_template/engine-detail-versions
{
    "index_patterns":  [
                           "engine-detail-*"
                       ],
    "template":  {
                     "settings":  {
                                      "index":  {
                                                    "number_of_shards":  "1",
                                                    "number_of_replicas":  "0"
                                                }
                                  },
                     "mappings":  {
                                      "properties":  {
                                                         "variables":  {
                                                                           "type":  "nested",
                                                                           "properties":  {
                                                                                              "salary":  {
                                                                                                             "type":  "double"
                                                                                                         },
                                                                                              "married":  {
                                                                                                              "type":  "boolean"
                                                                                                          },
                                                                                              "age":  {
                                                                                                          "type":  "integer"
                                                                                                      }
                                                                                          }
                                                                       },
                                                         "address":  {
                                                                         "type":  "text"
                                                                     },
                                                         "decision_result":  {
                                                                                 "type":  "keyword"
                                                                             },
                                                         "name":  {
                                                                      "type":  "text"
                                                                  },
                                                         "created_at":  {
                                                                            "type":  "date"
                                                                        },
                                                         "applicationID":  {
                                                                               "type":  "keyword"
                                                                           },
                                                         "debt":  {
                                                                      "type":  "double"
                                                                  }
                                                     }
                                  }
                 },
    "composed_of":  [

                    ],
    "priority":  200
}

PUT /_index_template/engine-summary-versions
{
    "index_patterns":  [
                           "engine-summary-v*"
                       ],
    "template":  {
                     "settings":  {
                                      "index":  {
                                                    "number_of_shards":  "1",
                                                    "number_of_replicas":  "0"
                                                }
                                  },
                     "mappings":  {
                                      "properties":  {
                                                         "address":  {
                                                                         "type":  "text"
                                                                     },
                                                         "decision_result":  {
                                                                                 "type":  "keyword"
                                                                             },
                                                         "name":  {
                                                                      "type":  "text"
                                                                  },
                                                         "created_at":  {
                                                                            "type":  "date"
                                                                        },
                                                         "applicationID":  {
                                                                               "type":  "keyword"
                                                                           },
                                                         "debt":  {
                                                                      "type":  "double"
                                                                  }
                                                     }
                                  }
                 },
    "composed_of":  [

                    ],
    "priority":  200
}
```

## 3. Membuat index dan memasang alias secara manual

Lewati langkah ini jika index sudah dibuat aplikasi. Template di atas otomatis memasang mapping.

```http
PUT /engine-detail-0001
{}
PUT /engine-detail-0002
{}
PUT /engine-detail-0003
{}
PUT /engine-summary-v001
{}

POST /_aliases
{
  "actions": [
    {"add": {"index": "engine-detail-0001", "alias": "engine-detail", "is_write_index": false}},
    {"add": {"index": "engine-detail-0002", "alias": "engine-detail", "is_write_index": false}},
    {"add": {"index": "engine-detail-0003", "alias": "engine-detail", "is_write_index": true}},
    {"add": {"index": "engine-summary-v001", "alias": "engine-summary", "is_write_index": true}}
  ]
}
```

Alias adalah nama logis, bukan salinan dokumen. Search lewat alias membaca seluruh anggotanya, termasuk versi lama. Write melalui alias diarahkan ke satu index yang `is_write_index: true`.

## 4. Mengisi dataset dan melihat write target

Jalankan Go lalu gunakan PowerShell:

```powershell
Invoke-RestMethod http://localhost:8080/dummy -Method Post -TimeoutSec 330
```

Generator mulai dari detail-0001, menulis 500 dokumen per bulk secara kronologis, lalu rollover saat 30.000 dokumen. Generator tidak menulis semua history ke versi terbaru saja: setiap versi lama terisi terlebih dahulu sebelum versi baru dibuat. Summary ditulis sesudah seluruh detail berhasil.
Response `data.index_documents` memuat jumlah per concrete index; `data.detail_write_index` dan `data.summary_write_index` menunjukkan target write terakhir.

## 5. Rollover otomatis pada 30.000 dokumen

Selama aplikasi Go berjalan, worker di `elastic_indexes.go` memeriksa setiap 30 detik:

1. Temukan semua versi konkret yang valid untuk kedua keluarga index.
2. Tambahkan versi tersebut ke alias baca dan tandai versi bernomor terbesar sebagai satu-satunya write index.
3. Refresh dan hitung root documents pada write index dengan `_count`.
4. Jika sudah mencapai 30.000, panggil `_rollover` dengan nama versi berikutnya secara eksplisit.
5. Elasticsearch membuat index baru dengan template, mengubah write index lama menjadi false, dan menandai versi baru sebagai true secara atomik.

Writer dummy juga memeriksa kapasitas sebelum/sesudah batch, membagi batch jika perlu sehingga tidak melewati 30.000 per versi. Count memakai dokumen utama; nested `variables` tidak dihitung sebagai row tambahan.
Untuk write dari aplikasi lain, pemeriksaan periodik dapat terlambat sampai 30 detik sehingga ambang bisa terlewati. Tulis melalui alias dan gunakan satu proses Go sebagai pengelola lifecycle; jangan jalankan dua generator/worker pada index yang sama.

Nama summary `engine-summary-v001` tidak mengikuti format penomoran otomatis ILM standar (`...-000001`). Karena itu solusi ini memakai worker Go dan Rollover API dengan nama eksplisit, bukan policy ILM. Worker berhenti saat aplikasi berhenti.

Contoh query setara (hanya rollover jika kondisi terpenuhi):

```http
POST /engine-detail/_refresh
POST /engine-detail/_rollover/engine-detail-0004
{
  "conditions": {"max_docs": 30000}
}

POST /engine-summary/_refresh
POST /engine-summary/_rollover/engine-summary-v002
{
  "conditions": {"max_docs": 30000}
}
```

Setelah dummy, detail-0003 dan summary-v001 baru berisi 20.000, jadi query kondisi tersebut belum membuat versi baru.
Jika membuat `engine-detail-0004` sendiri dengan PUT, worker akan menghubungkannya ke alias dan memindahkan write ke 0004 pada pemeriksaan berikutnya. Gunakan Rollover API untuk membuat versi berikutnya dan memindahkan alias dalam satu operasi.

## 6. Query operasional

```http
GET /_alias/engine-detail,engine-summary
GET /_cat/aliases/engine-detail,engine-summary?v&h=alias,index,is_write_index
GET /_cat/indices/engine-detail-*,engine-summary-v*?v
GET /engine-detail/_mapping
GET /engine-summary/_mapping

GET /engine-detail/_count
GET /engine-summary/_count
GET /engine-detail-0001/_count
GET /engine-detail-0002/_count
GET /engine-detail-0003/_count
GET /engine-summary-v001/_count

GET /engine-detail/_search
{
  "query": {"term": {"applicationID": "APP-000001"}},
  "sort": [{"created_at": "asc"}],
  "size": 10
}

GET /engine-summary/_search
{
  "query": {"term": {"applicationID": "APP-000001"}}
}

GET /engine-detail/_search
{
  "size": 0,
  "aggs": {
    "versions": {
      "terms": {"field": "_index", "size": 100},
      "aggs": {
        "oldest": {"min": {"field": "created_at"}},
        "newest": {"max": {"field": "created_at"}}
      }
    }
  }
}
```

`_cat/indices` dapat menghitung nested Lucene documents. Gunakan `_count` untuk jumlah row aplikasi (detail 80.000, summary 20.000).

## 7. Write baru dan update summary

Contoh format bulk ke alias (sesuaikan data, ini bukan bagian dataset dummy):

```http
POST /engine-detail/_bulk
{"index":{"_id":"APP-NEW-01"}}
{"applicationID":"APP-NEW","name":"Budi","debt":10000000,"address":"Jakarta","decision_result":"approve","created_at":"2026-09-29T10:00:00Z","variables":[{"age":35,"married":false,"salary":10000000}]}
```

Dokumen baru menuju write index terbaru. Gunakan `_id` unik untuk setiap occurrence detail.

**Alias multi-index tidak menjamin keunikan `_id` lintas index.** Untuk menjaga satu summary per applicationID setelah summary memiliki banyak versi:

1. Cari applicationID lewat alias summary dengan term query.
2. Jika belum ada, insert ke write alias dengan `_id = applicationID`.
3. Jika sudah ada, update concrete `_index` yang dikembalikan pencarian, dengan `_id` lama; jangan insert ulang lewat write alias karena dapat membuat duplikat pada versi baru.
4. Bandingkan `created_at` sebelum update agar history lama tidak menimpa latest state.

Generator mengikuti aturan keunikan dengan reset dataset dan satu write summary per applicationID. Endpoint GET summary berdasarkan id memakai search lintas alias; duplikat id dilaporkan sebagai error.
Endpoint GET tidak otomatis menyinkronkan summary ketika detail diisi langsung oleh proses eksternal.

Referensi: [Elasticsearch aliases](https://www.elastic.co/docs/manage-data/data-store/aliases), [Rollover API](https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-indices-rollover).
