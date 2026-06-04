export namespace main {
	
	export class AlbumData {
	    artist: string;
	    title: string;
	    year: number;
	    artBase64: string;
	    accentHex: string;
	    trackCount: number;
	
	    static createFrom(source: any = {}) {
	        return new AlbumData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.artist = source["artist"];
	        this.title = source["title"];
	        this.year = source["year"];
	        this.artBase64 = source["artBase64"];
	        this.accentHex = source["accentHex"];
	        this.trackCount = source["trackCount"];
	    }
	}
	export class PlaylistTrack {
	    index: number;
	    path: string;
	    title: string;
	    current: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PlaylistTrack(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.path = source["path"];
	        this.title = source["title"];
	        this.current = source["current"];
	    }
	}
	export class SongData {
	    path: string;
	    title: string;
	    artist: string;
	    album: string;
	    trackNum: number;
	
	    static createFrom(source: any = {}) {
	        return new SongData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.title = source["title"];
	        this.artist = source["artist"];
	        this.album = source["album"];
	        this.trackNum = source["trackNum"];
	    }
	}
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
	    artBase64: string;
	    accentHex: string;
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
	        this.artBase64 = source["artBase64"];
	        this.accentHex = source["accentHex"];
	        this.outputMode = source["outputMode"];
	    }
	}

}

