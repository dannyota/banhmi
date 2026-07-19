#!/bin/sh
set -e
case "${1:-eval}" in
  eval)
    shift 2>/dev/null || true
    exec banhmi-eval -golden /eval/golden.json "$@"
    ;;
  server)
    shift 2>/dev/null || true
    exec banhmi-server "$@"
    ;;
  *)
    exec "$@"
    ;;
esac
