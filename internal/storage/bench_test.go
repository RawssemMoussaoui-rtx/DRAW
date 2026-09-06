package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"draw/internal/model"
)

var benchIDSeq uint64

func benchDB(b *testing.B) *sql.DB {
	b.Helper()
	dir := b.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "draw-bench.db")) + "?mode=rwc&_pragma=busy_timeout%3d5000"
	db, err := Open(dsn)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(8)
	if err := Apply(context.Background(), db); err != nil {
		b.Fatalf("Apply: %v", err)
	}
	b.Cleanup(func() { db.Close() })
	return db
}

func BenchmarkSQLite_EvidenceThroughput(b *testing.B) {
	db := benchDB(b)
	es, err := NewSQLiteEvidenceStore(db)
	if err != nil {
		b.Fatalf("NewSQLiteEvidenceStore: %v", err)
	}

	const itemsPerG = 200
	for _, n := range []int{1, 4, 8} {
		b.Run(fmt.Sprintf("Put/goroutines=%d", n), func(b *testing.B) {
			totalOps := n * itemsPerG

			start := time.Now()
			var wg sync.WaitGroup
			wg.Add(n)
			for g := 0; g < n; g++ {
				go func() {
					defer wg.Done()
					for i := 0; i < itemsPerG; i++ {
						seq := atomic.AddUint64(&benchIDSeq, 1)
						ev := model.Evidence{
							ID:           model.EvidenceID(fmt.Sprintf("ev_%d", seq)),
							SessionID:    "ses_bench",
							TaskID:       "tsk_bench",
							SourceID:     "src_bench",
							Topic:        "bench",
							Claim:        "claim_bench",
							Value:        "value_bench",
							Confidence:   0.5,
							Verification: model.VerificationUnverified,
							CollectedAt:  time.Now().UTC(),
						}
						if _, pErr := es.Put(ev); pErr != nil {
							b.Errorf("Put: %v", pErr)
							return
						}
					}
				}()
			}
			wg.Wait()
			elapsed := time.Since(start)

			opsPerSec := float64(totalOps) / elapsed.Seconds()
			b.ReportMetric(opsPerSec, "ingest_ops_per_sec")

			qStart := time.Now()
			got := es.Query(EvidenceFilter{Topic: "bench"})
			queryMs := time.Since(qStart).Seconds() * 1000
			b.ReportMetric(queryMs, "query_ms")
			if len(got) == 0 {
				b.Errorf("Query returned no evidence")
			}
		})
	}
}

func BenchmarkSQLite_SourceThroughput(b *testing.B) {
	db := benchDB(b)
	reg, err := NewSQLiteSourceRegistry(db, 0)
	if err != nil {
		b.Fatalf("NewSQLiteSourceRegistry: %v", err)
	}

	const domainsPerG = 200
	for _, n := range []int{1, 4, 8} {
		b.Run(fmt.Sprintf("UpsertLookup/goroutines=%d", n), func(b *testing.B) {
			domains := make([]string, domainsPerG)
			for i := range domains {
				domains[i] = fmt.Sprintf("src-%d-%d.bench", n, i)
			}

			totalUpserts := n * domainsPerG
			var wg sync.WaitGroup
			wg.Add(n)
			upStart := time.Now()
			for g := 0; g < n; g++ {
				go func(off int) {
					defer wg.Done()
					for i := 0; i < domainsPerG; i++ {
						p := model.SourceProfile{
							Domain:          domains[(off+i)%len(domains)],
							Class:           model.SourceClassNews,
							QualityScore:    0.5,
							CrawlDepthLimit: 3,
							PerDomainLimit:  5,
							RateLimitRPM:    60,
							AuthType:        model.AuthTypeNone,
						}
						if uErr := reg.Upsert(p); uErr != nil {
							b.Errorf("Upsert: %v", uErr)
							return
						}
					}
				}(g * 7 % len(domains))
			}
			wg.Wait()
			upElapsed := time.Since(upStart)
			b.ReportMetric(float64(totalUpserts)/upElapsed.Seconds(), "upsert_ops_per_sec")

			totalLookups := n * domainsPerG
			lkStart := time.Now()
			wg.Add(n)
			for g := 0; g < n; g++ {
				go func() {
					defer wg.Done()
					for i := 0; i < domainsPerG; i++ {
						if _, ok := reg.Lookup(domains[i]); !ok {
							b.Errorf("Lookup missing: %s", domains[i])
							return
						}
					}
				}()
			}
			wg.Wait()
			lookupMs := time.Since(lkStart).Seconds() * 1000
			b.ReportMetric(float64(totalLookups), "lookup_ops")
			b.ReportMetric(lookupMs, "lookup_ms")
		})
	}
}
