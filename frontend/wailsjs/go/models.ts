export namespace node {
	
	export class Alias {
	    Name: string;
	    PeerID: string;
	    // Go type: time
	    LastSeen: any;
	
	    static createFrom(source: any = {}) {
	        return new Alias(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Name = source["Name"];
	        this.PeerID = source["PeerID"];
	        this.LastSeen = this.convertValues(source["LastSeen"], null);
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
	export class Message {
	    PeerID: string;
	    Body: string;
	    // Go type: time
	    Timestamp: any;
	    Direction: string;
	
	    static createFrom(source: any = {}) {
	        return new Message(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.PeerID = source["PeerID"];
	        this.Body = source["Body"];
	        this.Timestamp = this.convertValues(source["Timestamp"], null);
	        this.Direction = source["Direction"];
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
	export class PeerInfo {
	    PeerID: string;
	    Name: string;
	    Hostname: string;
	    Addrs: string[];
	    // Go type: time
	    LastSeen: any;
	    Online: boolean;
	    IsSelf: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PeerInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.PeerID = source["PeerID"];
	        this.Name = source["Name"];
	        this.Hostname = source["Hostname"];
	        this.Addrs = source["Addrs"];
	        this.LastSeen = this.convertValues(source["LastSeen"], null);
	        this.Online = source["Online"];
	        this.IsSelf = source["IsSelf"];
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

