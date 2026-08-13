package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/the-loon-clan/loon/core"
)

// serve runs the HTTP server until the process is asked to stop, then shuts it
// down in order.
//
// Split out of Main for the same reason as the boot phases: what happens on
// the way DOWN is as much a decision as what happens on the way up, and buried
// at the bottom of a thousand-line function nobody reads it. The order matters
// — stop accepting requests, then stop the runtime, then release the shared
// Redis client the host owns.
// redis is the shared client when REDIS_ADDR is set, nil otherwise. Passed in
// rather than reached for, because the host owns its lifecycle and closing it
// is the last thing that happens here.
func serve(engine *gin.Engine, wsrv *web, rt *core.Runtime, ctx context.Context,
	stop context.CancelFunc, logger *slog.Logger, redis *goredis.Client) {
	srv := &http.Server{Addr: ":8090", Handler: engine}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http", "err", err)
			stop()
		}
	}()
	// Pull down the art of releases matched before local caching existed. Slow
	// on purpose and cancelled with the process — see backfillCovers.
	go wsrv.backfillCovers(ctx, 2*time.Second, logger)
	// Give cover art to releases whose SERIES is already in the catalog. Costs
	// no API call, so it is not paced by anyone's rate limit — see
	// linkFromCatalog for why this runs ahead of the scraper's match job.
	go wsrv.runLocalLinks(ctx, time.Minute, logger)

	logger.Info("loon site up",
		"url", "http://localhost:8090/",
		"login", "alice/alice (admin) or bob/bob")

	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	rt.Stop(shutCtx)
	if redis != nil {
		_ = redis.Close() // host owns the shared client's lifecycle
	}
}
