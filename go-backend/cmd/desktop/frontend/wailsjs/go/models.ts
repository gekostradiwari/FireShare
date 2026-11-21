export namespace main {
	
	export class DeviceView {
	    id: string;
	    name: string;
	    createdAt: string;
	    lastSeen: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.createdAt = source["createdAt"];
	        this.lastSeen = source["lastSeen"];
	    }
	}
	export class PairingData {
	    code: string;
	    expiresAt: string;
	    payload: string;
	    qrBase64: string;
	    urls: string[];
	    fingerprint: string;
	
	    static createFrom(source: any = {}) {
	        return new PairingData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.expiresAt = source["expiresAt"];
	        this.payload = source["payload"];
	        this.qrBase64 = source["qrBase64"];
	        this.urls = source["urls"];
	        this.fingerprint = source["fingerprint"];
	    }
	}
	export class RemoteView {
	    portMapEnabled: boolean;
	    portMapState: string;
	    portMapMessage: string;
	    gateway: string;
	    externalIp: string;
	    cgnat: boolean;
	    duckdnsEnabled: boolean;
	    duckdnsState: string;
	    duckdnsMessage: string;
	    duckdnsDomain: string;
	    tailscale: string[];
	    publicUrls: string[];
	    hints: string[];
	    checkedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new RemoteView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.portMapEnabled = source["portMapEnabled"];
	        this.portMapState = source["portMapState"];
	        this.portMapMessage = source["portMapMessage"];
	        this.gateway = source["gateway"];
	        this.externalIp = source["externalIp"];
	        this.cgnat = source["cgnat"];
	        this.duckdnsEnabled = source["duckdnsEnabled"];
	        this.duckdnsState = source["duckdnsState"];
	        this.duckdnsMessage = source["duckdnsMessage"];
	        this.duckdnsDomain = source["duckdnsDomain"];
	        this.tailscale = source["tailscale"];
	        this.publicUrls = source["publicUrls"];
	        this.hints = source["hints"];
	        this.checkedAt = source["checkedAt"];
	    }
	}
	export class Settings {
	    serverName: string;
	    port: number;
	    rootDir: string;
	    publicUrls: string[];
	    shareBaseUrl: string;
	    autoPortMap: boolean;
	    duckdnsDomain: string;
	    duckdnsToken: string;
	    duckdnsTokenSet: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.serverName = source["serverName"];
	        this.port = source["port"];
	        this.rootDir = source["rootDir"];
	        this.publicUrls = source["publicUrls"];
	        this.shareBaseUrl = source["shareBaseUrl"];
	        this.autoPortMap = source["autoPortMap"];
	        this.duckdnsDomain = source["duckdnsDomain"];
	        this.duckdnsToken = source["duckdnsToken"];
	        this.duckdnsTokenSet = source["duckdnsTokenSet"];
	    }
	}
	export class ShareView {
	    id: string;
	    name: string;
	    path: string;
	    url: string;
	    createdBy: string;
	    createdAt: string;
	    expiresAt: string;
	    maxDownloads: number;
	    downloads: number;
	    hasPassword: boolean;
	    trustedUrl: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ShareView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.path = source["path"];
	        this.url = source["url"];
	        this.createdBy = source["createdBy"];
	        this.createdAt = source["createdAt"];
	        this.expiresAt = source["expiresAt"];
	        this.maxDownloads = source["maxDownloads"];
	        this.downloads = source["downloads"];
	        this.hasPassword = source["hasPassword"];
	        this.trustedUrl = source["trustedUrl"];
	    }
	}
	export class Status {
	    running: boolean;
	    error: string;
	    version: string;
	    serverName: string;
	    port: number;
	    rootDir: string;
	    fingerprint: string;
	    urls: string[];
	    devices: DeviceView[];
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.error = source["error"];
	        this.version = source["version"];
	        this.serverName = source["serverName"];
	        this.port = source["port"];
	        this.rootDir = source["rootDir"];
	        this.fingerprint = source["fingerprint"];
	        this.urls = source["urls"];
	        this.devices = this.convertValues(source["devices"], DeviceView);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

