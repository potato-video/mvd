# hls.js

`hls.light.min.js.gz` is [hls.js](https://github.com/video-dev/hls.js)
1.6.19's light build (`dist/hls.light.min.js` from the npm package,
`gzip -9n`), under the Apache License 2.0 in `LICENSE`. The website loads
it to play a remux in browsers without HLS of their own; only builds with
the `remux` feature have it. To update it:

```sh
npm pack hls.js@VERSION && tar -xzf hls.js-VERSION.tgz
gzip -9cn package/dist/hls.light.min.js > internal/server/hls/hls.light.min.js.gz
cp package/LICENSE internal/server/hls/LICENSE
```
