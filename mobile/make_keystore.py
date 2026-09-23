"""Generate the Android signing keystore.

keytool would be the normal tool, but this project deliberately needs no JDK
locally (CI is the only place that compiles anything Android), and a keystore
that apksigner/jarsigner accept is just PKCS12 -- which Python's `cryptography`
can write directly.

Run once, on the machine you keep the key on:

    pip install cryptography
    python mobile/make_keystore.py

Writes (all inside keystore/, which is gitignored):

    keystore/elite-mon.p12     the key itself  -- BACK THIS UP
    keystore/password.txt      the password, bare, pipeable into gh secret set
    keystore/keystore.b64      base64 of the .p12, for the CI secret

Three details that would otherwise bite later:

* The alias is all-lowercase on purpose. Java lowercases PKCS12 aliases, so a
  mixed-case friendly name comes back as a *different* alias and
  `apksigner --ks-key-alias` fails with "alias not found".
* PKCS12 has a single password slot, so the key password is the store password.
  The workflow passes the same secret for both.
* The password is alphanumeric only, so it never needs shell escaping on the way
  through `apksigner --ks-pass env:...`.

Losing the key means the app can never be updated in place again -- Android
refuses an APK signed with a different key, so every user would have to
uninstall first.
"""
import base64
import datetime
import os
import secrets
import string

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.hazmat.primitives.serialization import pkcs12
from cryptography.x509.oid import NameOID

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
OUT = os.path.join(ROOT, "keystore")
ALIAS = "elitemon"
PASSWORD = "".join(secrets.choice(string.ascii_letters + string.digits)
                   for _ in range(32))

os.makedirs(OUT, exist_ok=True)

key = rsa.generate_private_key(public_exponent=65537, key_size=4096)
name = x509.Name([
    x509.NameAttribute(NameOID.COMMON_NAME, "ED Monitor"),
    x509.NameAttribute(NameOID.ORGANIZATION_NAME, "yafeng"),
    x509.NameAttribute(NameOID.COUNTRY_NAME, "CN"),
])
now = datetime.datetime.now(datetime.timezone.utc)
# Google's own guidance for signing keys is a validity that outlives the app;
# 100 years means it never has to be thought about again.
cert = (x509.CertificateBuilder()
        .subject_name(name).issuer_name(name)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - datetime.timedelta(days=1))
        .not_valid_after(now + datetime.timedelta(days=36500))
        .add_extension(x509.BasicConstraints(ca=False, path_length=None),
                       critical=True)
        .sign(key, hashes.SHA256()))

blob = pkcs12.serialize_key_and_certificates(
    name=ALIAS.encode(),
    key=key,
    cert=cert,
    cas=None,
    encryption_algorithm=serialization.BestAvailableEncryption(PASSWORD.encode()),
)

p12 = os.path.join(OUT, "elite-mon.p12")
with open(p12, "wb") as f:
    f.write(blob)

# Bare values, no trailing newline: both are piped straight into
# `gh secret set <name> < file`, and a stray \n in the password would only
# surface much later as "keystore password was incorrect".
with open(os.path.join(OUT, "password.txt"), "w", encoding="ascii") as f:
    f.write(PASSWORD)
with open(os.path.join(OUT, "keystore.b64"), "w", encoding="ascii") as f:
    f.write(base64.b64encode(blob).decode())

print("keystore : %s (%d bytes)" % (p12, len(blob)))
print("alias    : %s" % ALIAS)
print("password : %s" % PASSWORD)
print("subject  : %s" % cert.subject.rfc4514_string())
print("valid    : %s .. %s" % (cert.not_valid_before_utc.date(),
                               cert.not_valid_after_utc.date()))
print("sha256   : %s" % cert.fingerprint(hashes.SHA256()).hex())
print()
print("next: gh secret set ANDROID_KEYSTORE_BASE64   < keystore/keystore.b64")
print("      gh secret set ANDROID_KEYSTORE_PASSWORD < keystore/password.txt")
print("      gh secret set ANDROID_KEY_PASSWORD      < keystore/password.txt")
print("      gh secret set ANDROID_KEY_ALIAS --body %s" % ALIAS)
