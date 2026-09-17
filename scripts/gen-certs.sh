#!/usr/bin/env bash
#
# Generates the self-signed certificate the demo edge presents.
#
# The certificate is generated locally and never committed: a repository that
# carries a private key trains everyone who clones it to do the same. In a real
# deployment this script is replaced by certbot or by a certificate issued
# through the platform's ACME integration.
set -euo pipefail

CERT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/nginx/certs"
DAYS="${CERT_DAYS:-365}"
HOSTNAME_CN="${CERT_CN:-localhost}"

mkdir -p "$CERT_DIR"

if [[ -f "$CERT_DIR/edge.crt" && -f "$CERT_DIR/edge.key" ]]; then
  if openssl x509 -checkend 86400 -noout -in "$CERT_DIR/edge.crt" >/dev/null 2>&1; then
    echo "certificate already present and valid: $CERT_DIR/edge.crt"
    exit 0
  fi
  echo "existing certificate is expired or expiring within 24h, regenerating"
fi

# A SAN is mandatory: modern clients ignore the legacy CN field entirely, so a
# certificate without subjectAltName fails verification everywhere.
openssl req -x509 -nodes -newkey rsa:2048 \
  -keyout "$CERT_DIR/edge.key" \
  -out "$CERT_DIR/edge.crt" \
  -days "$DAYS" \
  -subj "/CN=${HOSTNAME_CN}/O=Realtime Edge Demo" \
  -addext "subjectAltName=DNS:${HOSTNAME_CN},DNS:nginx,IP:127.0.0.1" \
  -addext "keyUsage=digitalSignature,keyEncipherment" \
  -addext "extendedKeyUsage=serverAuth" \
  2>/dev/null

# The key must not be world-readable. nginx reads it as root before dropping
# privileges, so 600 is sufficient and strictly better than the default.
chmod 600 "$CERT_DIR/edge.key"
chmod 644 "$CERT_DIR/edge.crt"

echo "generated self-signed certificate:"
openssl x509 -in "$CERT_DIR/edge.crt" -noout -subject -dates -ext subjectAltName | sed 's/^/  /'
