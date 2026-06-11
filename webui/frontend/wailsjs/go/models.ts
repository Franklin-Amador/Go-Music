export namespace audio {
	
	export class OutputDevice {
	    id: string;
	    name: string;
	    isDefault: boolean;
	
	    static createFrom(source: any = {}) {
	        return new OutputDevice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.isDefault = source["isDefault"];
	    }
	}

}

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
	export class PlaylistMeta {
	    name: string;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new PlaylistMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.count = source["count"];
	    }
	}
	export class PlaylistTrack {
	    index: number;
	    path: string;
	    title: string;
	    current: boolean;
	    duration: number;
	
	    static createFrom(source: any = {}) {
	        return new PlaylistTrack(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.path = source["path"];
	        this.title = source["title"];
	        this.current = source["current"];
	        this.duration = source["duration"];
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
	export class webConfig {
	    volume: number;
	    playlist: string[];
	    current: number;
	    exclusive: boolean;
	    crossfade: number;
	    shuffle: boolean;
	    repeat: boolean;
	    musicRoot: string;
	    visualizerMode: string;
	    accentSource: string;
	    outputDevice: string;
	
	    static createFrom(source: any = {}) {
	        return new webConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.volume = source["volume"];
	        this.playlist = source["playlist"];
	        this.current = source["current"];
	        this.exclusive = source["exclusive"];
	        this.crossfade = source["crossfade"];
	        this.shuffle = source["shuffle"];
	        this.repeat = source["repeat"];
	        this.musicRoot = source["musicRoot"];
	        this.visualizerMode = source["visualizerMode"];
	        this.accentSource = source["accentSource"];
	        this.outputDevice = source["outputDevice"];
	    }
	}

}

