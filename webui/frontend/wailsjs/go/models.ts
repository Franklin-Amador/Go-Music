export namespace main {
	
	export class TrackInfo {
	    path: string;
	    title: string;
	    artist: string;
	    album: string;
	    format: string;
	    qualityLabel: string;
	    dsdLabel: string;
	    duration: number;
	    isDSD: boolean;
	    hasArt: boolean;
	    pictureMIME: string;
	    outputMode: string;
	
	    static createFrom(source: any = {}) {
	        return new TrackInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.title = source["title"];
	        this.artist = source["artist"];
	        this.album = source["album"];
	        this.format = source["format"];
	        this.qualityLabel = source["qualityLabel"];
	        this.dsdLabel = source["dsdLabel"];
	        this.duration = source["duration"];
	        this.isDSD = source["isDSD"];
	        this.hasArt = source["hasArt"];
	        this.pictureMIME = source["pictureMIME"];
	        this.outputMode = source["outputMode"];
	    }
	}

}

