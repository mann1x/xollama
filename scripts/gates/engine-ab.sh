#!/bin/bash
cd /srv/dev-disk-by-label-opt/dev/xollama
exec sudo -u ollama env HOME=/srv/ml/xollama-phase2/as-ollama/home python3 scripts/phase2-engine-ab.py --engine opencoti --axis all --models /srv/dev-disk-by-uuid-92295e2c-12bd-4d15-a50c-1d80e1a33ee8/spool/ollama_models --artifact /srv/ml/xb140/stage-x86_64/opencoti-0.10.5-c7-2610041714001 --out /srv/ml/xollama-phase2/as-ollama/pin-b140
