// Share-via-URL: the program source travels base64url-encoded in the hash
// (#code=...). Plain base64 keeps decoding synchronous for instant startup;
// playground programs are tiny.

const PREFIX = "#code="

export function encodeSourceToHash(source: string): string {
  const bytes = new TextEncoder().encode(source)
  let binary = ""
  for (const byte of bytes) binary += String.fromCharCode(byte)
  const base64url = btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "")
  return PREFIX + base64url
}

export function decodeSourceFromHash(hash: string = window.location.hash): string | null {
  if (!hash.startsWith(PREFIX)) return null
  try {
    const base64 = hash.slice(PREFIX.length).replaceAll("-", "+").replaceAll("_", "/")
    const binary = atob(base64)
    const bytes = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
    return new TextDecoder().decode(bytes)
  } catch {
    return null
  }
}
