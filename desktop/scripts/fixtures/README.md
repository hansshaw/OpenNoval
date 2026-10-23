# Local media acceptance fixtures

These original synthetic fixtures contain a solid green frame, a one-second
green video, and half a second of silence. They exercise decoding, same-origin
playback and byte ranges without external content or paid model requests.

Generated with FFmpeg 8.0.1:

```sh
ffmpeg -f lavfi -i 'color=c=0x39765e:s=32x32:d=1:r=10' -c:v libx264 -pix_fmt yuv420p -an -movflags +faststart -map_metadata -1 media.mp4
ffmpeg -f lavfi -i 'anullsrc=r=24000:cl=mono' -t 0.5 -c:a libmp3lame -b:a 48k -write_xing 0 -id3v2_version 0 -map_metadata -1 media.mp3
ffmpeg -f lavfi -i 'color=c=0x39765e:s=32x32:d=1:r=10' -frames:v 1 media.png
```

FFmpeg is only used to create these tiny fixtures, not needed to run acceptance
tests, generate media through configured services, or run the desktop app.
