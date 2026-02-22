import sys
import struct
import pvporcupine

porcupine = pvporcupine.create(
    access_key=sys.argv[1],
    keyword_paths=[sys.argv[2]]
)

frame_length = porcupine.frame_length

while True:
    data = sys.stdin.buffer.read(frame_length * 2)
    if len(data) < frame_length * 2:
        break
    frame = struct.unpack_from(f"{frame_length}h", data)
    if porcupine.process(frame) >= 0:
        print("WAKE", flush=True)
        break

porcupine.delete()
