#!/usr/bin/env bash
#
# Generates the certificates the broker's TLS listener and the amqps tests need.
# Run before `docker compose -f ci/brokers.yml up rabbitmq`; the broker-integration
# workflow runs it as ci/<broker>/setup.sh.
#
# A private CA rather than a self-signed leaf, because the tests use it two ways:
# as the client's `ca_cert` (which must verify the server's name), and by its
# absence, where an amqps connection with no trust configured must *fail*. Both
# need a chain the system store does not know.
#
# Idempotent: a certificate that is still good is left alone, so a local run does
# not invalidate a broker that is already up. One inside a month of expiry is
# reissued, because the alternative is a working checkout that starts failing
# verification a year from now for no reason its output would explain.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
certs="certs"

if [ -f "$certs/server.pem" ] && openssl x509 -checkend 2592000 -noout -in "$certs/server.pem" >/dev/null 2>&1; then
    echo "certificates already present in ci/rabbitmq/$certs; nothing to do"
    exit 0
fi
rm -f "$certs"/*.pem "$certs"/*.srl

mkdir -p "$certs"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/ca.cnf" <<'EOF'
[req]
distinguished_name = dn
prompt = no
[dn]
CN = Vinculum integration-test CA
[ext]
basicConstraints = critical,CA:TRUE
keyUsage = critical,keyCertSign,cRLSign
EOF

# The SANs are the names a test can reach this broker by: localhost from the
# runner (RABBITMQ_TLS_SERVERNAME), and the compose hostname from inside the
# network. A name missing here is a verification failure that looks like a
# broken client.
cat >"$tmp/server.cnf" <<'EOF'
[req]
distinguished_name = dn
prompt = no
[dn]
CN = localhost
[ext]
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:localhost,DNS:vinculum-rabbitmq,IP:127.0.0.1
EOF

openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 3650 \
    -keyout "$certs/ca-key.pem" -out "$certs/ca.pem" \
    -config "$tmp/ca.cnf" -extensions ext 2>/dev/null

openssl req -newkey rsa:2048 -nodes -sha256 \
    -keyout "$certs/server-key.pem" -out "$tmp/server.csr" \
    -config "$tmp/server.cnf" 2>/dev/null

# 398 days, not the CA's ten years: a leaf living longer than that is refused
# outright by Apple's verifier, which is the one Go uses on a developer's Mac
# whenever the trust store is the system one. The refusal is correct — the
# system-trust test wants a failure — but it arrives as "certificate is not
# standards compliant", which reads like a broken generator rather than the
# untrusted CA the test is actually about.
openssl x509 -req -in "$tmp/server.csr" -sha256 -days 398 \
    -CA "$certs/ca.pem" -CAkey "$certs/ca-key.pem" -CAcreateserial \
    -extfile "$tmp/server.cnf" -extensions ext \
    -out "$certs/server.pem" 2>/dev/null

# The broker reads these as a different uid than the one that wrote them, and a
# key it cannot read stops the whole node rather than just the TLS listener.
chmod 644 "$certs"/*.pem

echo "issued ci/rabbitmq/$certs/{ca,server}.pem"
