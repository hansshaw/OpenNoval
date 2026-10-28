#!/usr/bin/env python3
"""Create the persistent Android release key in a private directory, once."""
import os
from pathlib import Path
import secrets
import shutil
import subprocess

directory = Path(os.environ.get("AINOVEL_SIGNING_DIR", Path.home() / ".local/share/ainovel/android-signing"))
directory.mkdir(parents=True, exist_ok=True, mode=0o700)
directory.chmod(0o700)
keystore = directory / "ainovel-release.p12"
password = directory / "keystore-password"
if keystore.exists() or password.exists():
    if not keystore.exists() or not password.exists():
        raise SystemExit("Signing material is incomplete. Restore it from your backup; refusing to replace an existing identity.")
    print(f"Reusing existing Android release signing material in {directory}")
    raise SystemExit(0)
keytool = shutil.which("keytool")
if not keytool:
    raise SystemExit("keytool not found. Source mobile/scripts/env.sh or install JDK 17.")
secret = secrets.token_urlsafe(48)
descriptor = os.open(password, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(descriptor, "w") as output:
    output.write(secret + "\n")
environment = dict(os.environ, AINOVEL_KEYSTORE_PASSWORD=secret)
subprocess.run([
    keytool, "-genkeypair", "-keystore", str(keystore), "-storetype", "PKCS12",
    "-storepass:env", "AINOVEL_KEYSTORE_PASSWORD", "-keypass:env", "AINOVEL_KEYSTORE_PASSWORD",
    "-alias", "ainovel-android-release", "-keyalg", "RSA", "-keysize", "3072",
    "-validity", "10000", "-dname", "CN=AI Novel Studio Android, OU=Release",
], env=environment, check=True)
keystore.chmod(0o600)
print(f"Created Android release signing material in {directory}")
print("Keep a private backup of this directory. It is required to sign compatible app updates.")
