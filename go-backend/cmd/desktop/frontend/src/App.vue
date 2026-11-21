<script setup>
import {computed, onBeforeUnmount, onMounted, reactive, ref} from 'vue'
import {
  CheckRemote,
  GetRemoteStatus,
  GetSettings,
  GetShares,
  GetStatus,
  OpenCloudFolder,
  RevokeDevice,
  RevokeShare,
  SaveSettings,
  StartPairing,
} from '../wailsjs/go/main/App'
import {EventsOff, EventsOn} from '../wailsjs/runtime/runtime'

const status = ref({running: false, error: '', version: '', serverName: '', port: 0, rootDir: '', fingerprint: '', urls: [], devices: []})
const pairing = ref(null)
const now = ref(Date.now())
const uploads = reactive({})        // id -> progresso in corso
const recent = ref([])              // ultimi file ricevuti
const settings = reactive({
  serverName: '', port: 8443, rootDir: '', publicUrlsText: '',
  shareBaseUrl: '', autoPortMap: false, duckdnsDomain: '', duckdnsToken: '', duckdnsTokenSet: false,
})
const remote = ref(null)
const checking = ref(false)
const shares = ref([])
const showSettings = ref(false)
const message = ref('')
let timer = null
let pollTimer = null

const secondsLeft = computed(() => {
  if (!pairing.value) return 0
  return Math.max(0, Math.round((new Date(pairing.value.expiresAt).getTime() - now.value) / 1000))
})
const pairingActive = computed(() => pairing.value && secondsLeft.value > 0)
const activeUploads = computed(() => Object.values(uploads))

function fmtSize(n) {
  if (n < 1024) return n + ' B'
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 ? 0 : 1) + ' ' + units[i]
}
function fmtTime(iso) {
  const d = new Date(iso)
  return isNaN(d) ? '' : d.toLocaleString()
}
function pct(u) {
  return u.size > 0 ? Math.min(100, Math.round((u.offset / u.size) * 100)) : 100
}

async function refresh() {
  try { status.value = await GetStatus() } catch (e) { message.value = String(e) }
}
async function loadSettings() {
  try {
    const s = await GetSettings()
    settings.serverName = s.serverName
    settings.port = s.port
    settings.rootDir = s.rootDir
    settings.publicUrlsText = (s.publicUrls || []).join('\n')
    settings.shareBaseUrl = s.shareBaseUrl || ''
    settings.autoPortMap = !!s.autoPortMap
    settings.duckdnsDomain = s.duckdnsDomain || ''
    settings.duckdnsToken = ''
    settings.duckdnsTokenSet = !!s.duckdnsTokenSet
  } catch (e) { message.value = String(e) }
}
async function loadRemote() {
  try { remote.value = await GetRemoteStatus() } catch (e) { message.value = String(e) }
}
async function checkRemote() {
  checking.value = true
  try { remote.value = await CheckRemote(); await refresh() } catch (e) { message.value = String(e) }
  finally { checking.value = false }
}
async function loadShares() {
  try { shares.value = await GetShares() } catch (e) { message.value = String(e) }
}
async function revokeShare(sh) {
  if (!confirm(`Revocare il link a "${sh.name}"? Smetterà di funzionare subito.`)) return
  await RevokeShare(sh.id)
  await loadShares()
}
function stateIcon(state) {
  return state === 'mapped' || state === 'ok' ? '✅' : state === 'failed' ? '⚠️' : state === 'searching' ? '⏳' : '⚪'
}
async function newPairing() {
  message.value = ''
  try { pairing.value = await StartPairing() } catch (e) { message.value = String(e) }
}
async function revoke(d) {
  if (!confirm(`Disconnettere "${d.name}"? Il telefono dovrà essere accoppiato di nuovo.`)) return
  await RevokeDevice(d.id)
  await refresh()
}
async function saveSettings() {
  message.value = ''
  try {
    await SaveSettings({
      serverName: settings.serverName,
      port: Number(settings.port),
      rootDir: settings.rootDir,
      publicUrls: settings.publicUrlsText.split('\n').map(s => s.trim()).filter(Boolean),
      shareBaseUrl: settings.shareBaseUrl,
      autoPortMap: settings.autoPortMap,
      duckdnsDomain: settings.duckdnsDomain,
      duckdnsToken: settings.duckdnsToken,
      duckdnsTokenSet: settings.duckdnsTokenSet,
    })
    pairing.value = null
    message.value = 'Impostazioni salvate: server riavviato.'
    await refresh()
    await loadSettings()
    await loadRemote()
  } catch (e) { message.value = String(e) }
}

onMounted(async () => {
  await refresh()
  await loadSettings()
  await loadRemote()
  await loadShares()
  timer = setInterval(() => { now.value = Date.now() }, 1000)
  pollTimer = setInterval(() => { loadRemote(); loadShares() }, 15000)
  EventsOn('upload:progress', p => { uploads[p.id] = p })
  EventsOn('upload:complete', c => {
    for (const id of Object.keys(uploads)) {
      if (uploads[id].name === c.file.name || uploads[id].offset >= uploads[id].size) delete uploads[id]
    }
    recent.value = [c, ...recent.value].slice(0, 20)
  })
  EventsOn('device:paired', async () => { pairing.value = null; await refresh() })
  EventsOn('server:error', refresh)
})
onBeforeUnmount(() => {
  clearInterval(timer)
  clearInterval(pollTimer)
  ;['upload:progress', 'upload:complete', 'device:paired', 'server:error'].forEach(e => EventsOff(e))
})
</script>

<template>
  <main>
    <header>
      <h1>🔥 FireShare</h1>
      <span class="badge" :class="status.running ? 'ok' : 'ko'">
        {{ status.running ? 'Server attivo' : 'Server fermo' }}
      </span>
    </header>

    <p v-if="status.error" class="error">{{ status.error }}</p>
    <p v-if="message" class="info">{{ message }}</p>

    <section v-if="status.running" class="card">
      <h2>Il tuo spazio cloud</h2>
      <p class="muted">Cartella: <code>{{ status.rootDir }}</code>
        <button class="link" @click="OpenCloudFolder()">Apri</button></p>
      <p class="muted">Indirizzi del server (porta {{ status.port }}):</p>
      <ul class="plain">
        <li v-for="u in status.urls" :key="u"><code>{{ u }}</code></li>
        <li v-if="!status.urls.length" class="muted">Nessuna rete rilevata.</li>
      </ul>
      <p class="muted small">Impronta del certificato (SHA-256): <code>{{ status.fingerprint }}</code></p>
    </section>

    <section class="card">
      <h2>Accoppia un telefono</h2>
      <div v-if="pairingActive" class="pairing">
        <img :src="pairing.qrBase64" alt="QR code di accoppiamento" width="260" height="260"/>
        <div>
          <p>Apri FireShare sul telefono e inquadra il QR.</p>
          <p class="code">{{ pairing.code }}</p>
          <p class="muted">Scade tra {{ secondsLeft }} s · monouso</p>
          <details>
            <summary>Collegamento manuale</summary>
            <textarea readonly rows="4" :value="pairing.payload"></textarea>
          </details>
        </div>
      </div>
      <p v-else class="muted">Il telefono deve essere sulla stessa rete del PC, oppure il PC deve essere raggiungibile da Internet (vedi Impostazioni).</p>
      <button class="btn" :disabled="!status.running" @click="newPairing">
        {{ pairingActive ? 'Genera un nuovo QR' : 'Mostra QR di accoppiamento' }}
      </button>
    </section>

    <section class="card">
      <h2>Dispositivi accoppiati</h2>
      <p v-if="!status.devices.length" class="muted">Nessun dispositivo.</p>
      <table v-else>
        <thead><tr><th>Nome</th><th>Accoppiato</th><th>Ultimo accesso</th><th></th></tr></thead>
        <tbody>
          <tr v-for="d in status.devices" :key="d.id">
            <td>{{ d.name }}</td>
            <td>{{ fmtTime(d.createdAt) }}</td>
            <td>{{ fmtTime(d.lastSeen) }}</td>
            <td><button class="btn danger" @click="revoke(d)">Disconnetti</button></td>
          </tr>
        </tbody>
      </table>
    </section>

    <section class="card">
      <h2>Trasferimenti</h2>
      <div v-for="u in activeUploads" :key="u.id" class="transfer">
        <div class="row"><span>{{ u.name }}</span><span class="muted">{{ u.deviceName }} · {{ fmtSize(u.offset) }} / {{ fmtSize(u.size) }}</span></div>
        <div class="bar"><div :style="{width: pct(u) + '%'}"></div></div>
      </div>
      <p v-if="!activeUploads.length" class="muted">Nessun trasferimento in corso.</p>
      <h3 v-if="recent.length">Ricevuti di recente</h3>
      <ul class="plain">
        <li v-for="(c, i) in recent" :key="i">✔ <code>{{ c.file.path }}</code>
          <span class="muted"> · {{ fmtSize(c.file.size) }} da {{ c.deviceName }}</span></li>
      </ul>
    </section>

    <section class="card">
      <h2>Accesso da fuori casa</h2>
      <template v-if="remote">
        <ul class="plain">
          <li>{{ stateIcon(remote.portMapState) }} <b>Porta sul router</b>
            <span class="muted"> · {{ remote.portMapMessage || 'non attivo' }}</span></li>
          <li>{{ stateIcon(remote.duckdnsState) }} <b>Nome DuckDNS</b>
            <span class="muted"> · {{ remote.duckdnsMessage || 'non configurato' }}</span></li>
          <li v-if="remote.tailscale.length">✅ <b>Tailscale</b>
            <span class="muted"> · {{ remote.tailscale.join(', ') }}</span></li>
          <li v-if="remote.externalIp" class="muted small">IP pubblico: <code>{{ remote.externalIp }}</code></li>
        </ul>
        <div v-if="remote.publicUrls.length">
          <p class="muted small">Indirizzi trovati automaticamente (il telefono li riceve da solo):</p>
          <ul class="plain"><li v-for="u in remote.publicUrls" :key="u"><code>{{ u }}</code></li></ul>
        </div>
        <p v-for="h in remote.hints" :key="h" class="hint">💡 {{ h }}</p>
      </template>
      <button class="btn" :disabled="checking || !status.running" @click="checkRemote">
        {{ checking ? 'Verifica in corso…' : 'Verifica ora' }}
      </button>
      <p class="muted small">Attiva le opzioni in Impostazioni → «Accesso da fuori casa». Se il tuo provider usa il
        CGNAT, nessuna porta si può aprire: in quel caso usa Tailscale (gratuito) sul PC e sul telefono.</p>
    </section>

    <section class="card">
      <h2>Link di condivisione</h2>
      <p v-if="!shares.length" class="muted">Nessun link attivo. Si creano dall'app sul telefono, dal menu di un file.</p>
      <table v-else>
        <thead><tr><th>File</th><th>Scade</th><th>Download</th><th></th></tr></thead>
        <tbody>
          <tr v-for="sh in shares" :key="sh.id">
            <td>{{ sh.name }} <span v-if="sh.hasPassword" title="Protetto da password">🔒</span></td>
            <td>{{ fmtTime(sh.expiresAt) }}</td>
            <td>{{ sh.downloads }}{{ sh.maxDownloads ? ' / ' + sh.maxDownloads : '' }}</td>
            <td><button class="btn danger" @click="revokeShare(sh)">Revoca</button></td>
          </tr>
        </tbody>
      </table>
    </section>

    <section class="card">
      <h2 class="clickable" @click="showSettings = !showSettings">Impostazioni {{ showSettings ? '▾' : '▸' }}</h2>
      <div v-if="showSettings" class="settings">
        <label>Nome del server<input v-model="settings.serverName" type="text"/></label>
        <label>Porta HTTPS<input v-model="settings.port" type="number" min="1" max="65535"/></label>
        <label>Cartella dello spazio cloud<input v-model="settings.rootDir" type="text"/></label>
        <label>Indirizzi pubblici (uno per riga, es. <code>https://miopc.duckdns.org:8443</code>)
          <textarea v-model="settings.publicUrlsText" rows="3"></textarea></label>
        <p class="muted small">Gli indirizzi pubblici vengono inclusi nel QR di accoppiamento.</p>

        <h3>Accesso da fuori casa</h3>
        <label class="check"><input v-model="settings.autoPortMap" type="checkbox"/>
          Apri automaticamente la porta sul router (UPnP)</label>
        <label>Nome DuckDNS (solo la parte prima di <code>.duckdns.org</code>, es. <code>miopc</code>)
          <input v-model="settings.duckdnsDomain" type="text" placeholder="miopc"/></label>
        <label v-if="settings.duckdnsDomain">Token DuckDNS
          <input v-model="settings.duckdnsToken" type="password" autocomplete="off"
                 :placeholder="settings.duckdnsTokenSet ? 'già salvato: lascia vuoto per non cambiarlo' : 'incolla il token da duckdns.org'"/></label>
        <label>Indirizzo base dei link di condivisione (facoltativo, es. <code>https://link.miodominio.it</code>)
          <input v-model="settings.shareBaseUrl" type="text" placeholder="https://…"/></label>
        <p class="muted small">Senza questo indirizzo i link usano il certificato autofirmato di FireShare: chi li
          apre vedrà un avviso del browser. Con un dominio tuo e un certificato valido (per esempio dietro un
          reverse proxy) l'avviso sparisce.</p>
        <button class="btn" @click="saveSettings">Salva e riavvia il server</button>
      </div>
    </section>
  </main>
</template>

<style>
main { max-width: 860px; margin: 0 auto; padding: 24px; text-align: left; }
header { display: flex; align-items: center; gap: 16px; }
h1 { margin: 0; font-size: 26px; }
h2 { margin: 0 0 10px; font-size: 18px; }
h3 { margin: 16px 0 6px; font-size: 14px; color: #9ca3af; }
.card { background: #1f2937; border-radius: 12px; padding: 16px 20px; margin-top: 16px; }
.muted { color: #9ca3af; }
.small { font-size: 12px; }
.badge { padding: 3px 10px; border-radius: 999px; font-size: 13px; }
.badge.ok { background: #065f46; }
.badge.ko { background: #7f1d1d; }
.error { background: #7f1d1d; padding: 10px 14px; border-radius: 8px; }
.info { background: #1e3a8a; padding: 10px 14px; border-radius: 8px; }
code { background: #111827; padding: 1px 6px; border-radius: 4px; word-break: break-all; }
ul.plain { list-style: none; padding: 0; margin: 4px 0; }
ul.plain li { padding: 2px 0; }
.btn { margin-top: 12px; padding: 9px 16px; border: 0; border-radius: 8px; background: #2563eb; color: white; cursor: pointer; }
.btn:disabled { opacity: .5; cursor: default; }
.btn.danger { margin: 0; background: #991b1b; padding: 5px 10px; }
.link { background: none; border: 0; color: #60a5fa; cursor: pointer; }
.pairing { display: flex; gap: 20px; align-items: center; flex-wrap: wrap; }
.pairing img { background: white; padding: 8px; border-radius: 10px; }
.code { font-size: 30px; letter-spacing: 4px; font-weight: 700; margin: 4px 0; }
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid #374151; font-size: 14px; }
.transfer { margin: 10px 0; }
.row { display: flex; justify-content: space-between; gap: 12px; font-size: 14px; }
.bar { height: 8px; background: #374151; border-radius: 4px; overflow: hidden; margin-top: 4px; }
.bar > div { height: 100%; background: #22c55e; transition: width .2s; }
.clickable { cursor: pointer; user-select: none; }
.hint { background: #1e3a8a; padding: 8px 12px; border-radius: 8px; font-size: 14px; margin: 8px 0; }
.settings label.check { display: flex; align-items: center; gap: 8px; }
.settings label.check input { width: auto; margin: 0; }
.settings label { display: block; margin: 10px 0; font-size: 14px; }
.settings input, .settings textarea, details textarea { display: block; width: 100%; box-sizing: border-box; margin-top: 4px; padding: 8px; border-radius: 6px; border: 1px solid #374151; background: #111827; color: white; }
</style>
