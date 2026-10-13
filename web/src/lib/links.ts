// What a link is, without anything else of the app's: the mock API uses it too.

/** A torrent: BitTorrent reaches its peers directly, so it can't go through Cloudflare WARP (as the daemon's downloads.IsBitTorrent). */
export const isTorrent = (link: string) => {
  const l = link.toLowerCase().split('?')[0]
  return l.startsWith('magnet:') || l.endsWith('.torrent')
}

/** Whether a link goes through Cloudflare WARP unless asked otherwise: YouTube, which blocks cloud servers (as downloads.DefaultWARP). */
export function warpByDefault(link: string) {
  if (isTorrent(link)) return false
  let host = link.toLowerCase().replace(/^[a-z][a-z0-9+.-]*:\/\//, '').split('/')[0]
  host = host.slice(host.lastIndexOf('@') + 1).split(':')[0]
  return ['youtube.com', 'youtu.be', 'youtube-nocookie.com'].some((s) => host === s || host.endsWith(`.${s}`))
}
