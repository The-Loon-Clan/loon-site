@echo off
REM Local-dev refresh for the loon demo site (Windows twin of update.sh).
REM Rebuilds the app image from the CURRENT local checkouts and restarts it,
REM then prunes the dangling old image layers the rebuild orphaned. No git pull:
REM loon / loon-baseline / loon-plugins are wired as BuildKit named build-
REM contexts in docker-compose.yml, so whatever is on disk is what gets built.
REM
REM Usage:  update.bat          rebuild + restart + prune (app on :8090)
REM         update.bat --logs   ...then tail the app logs
setlocal
cd /d "%~dp0"

echo == Rebuilding + restarting (app on http://localhost:8090) ...
REM --build is essential: a plain 'up' reuses the stale image.
docker compose up --build -d
if errorlevel 1 (
    echo Build/restart failed.
    exit /b 1
)

echo == Pruning dangling images left by previous builds ...
REM Dangling only (no -a): drops the orphaned old app layers, keeps base images.
docker image prune -f

echo == Status:
docker compose ps

if /i "%~1"=="--logs" (
    echo == Tailing app logs ^(Ctrl-C to stop^) ...
    docker compose logs -f app
) else (
    echo == Done. Tail logs with:  docker compose logs -f app
)
