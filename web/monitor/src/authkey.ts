// Seals a Tailscale auth key for a Client link, so a link that outlives its
// subscription does not give the key away. The key it is sealed under comes
// from the salt alone, and the hub keeps a copy of it only while the salt's
// subscription stands: delete the group or change its salt and the copy goes
// with the subscription files, leaving an old link nothing to open it with.
// Only the link is sealed; the profile it fetches carries the key in the clear,
// because the client has to use it.
//
// The scheme has to match the subscription service byte for byte:
//   key   = HKDF-SHA256(ikm = trim(salt), salt = empty, info = HKDF_INFO, 32 bytes)
//   value = "v1." + base64url, unpadded, of nonce(12) || AES-256-GCM ciphertext || tag(16)
// with the hostname, exactly as the link sends it, as the additional data, so
// a sealed key cannot be lifted into a link for another device.

const HKDF_INFO = "singbox-deploy/tailscale-authkey/v1";
const PREFIX = "v1.";
const NONCE_BYTES = 12;

const utf8 = new TextEncoder();

// WebCrypto exists only in a secure context: a dashboard reached over plain
// HTTP by address has no crypto.subtle, and cannot seal anything.
export function authKeyCryptoAvailable(): boolean {
  return typeof crypto !== "undefined" && !!crypto.subtle;
}

function base64url(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// A nonce is drawn fresh for every call unless one is given, which only a test
// does: the same key sealed twice makes two different values.
export async function encryptAuthKey(
  salt: string,
  hostname: string,
  authKey: string,
  nonce?: Uint8Array<ArrayBuffer>,
): Promise<string> {
  const iv = nonce ?? crypto.getRandomValues(new Uint8Array(NONCE_BYTES));
  if (iv.length !== NONCE_BYTES) throw new Error(`nonce must be ${NONCE_BYTES} bytes`);

  const ikm = await crypto.subtle.importKey("raw", utf8.encode(salt.trim()), "HKDF", false, ["deriveKey"]);
  const key = await crypto.subtle.deriveKey(
    { name: "HKDF", hash: "SHA-256", salt: new Uint8Array(0), info: utf8.encode(HKDF_INFO) },
    ikm,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt"],
  );
  // AES-GCM hands back the ciphertext with its 16-byte tag already appended.
  const sealed = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv, additionalData: utf8.encode(hostname), tagLength: 128 },
    key,
    utf8.encode(authKey),
  );

  const out = new Uint8Array(iv.length + sealed.byteLength);
  out.set(iv, 0);
  out.set(new Uint8Array(sealed), iv.length);
  return PREFIX + base64url(out);
}
