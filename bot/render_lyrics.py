#!/usr/bin/env python3
import sys
import os
import json
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from PIL import Image, ImageDraw, ImageFont, ImageFilter

def get_font_path():
    candidates = [
        "/usr/share/fonts/google-noto-sans-cjk-vf-fonts/NotoSansCJK-VF.ttc",
        "/usr/share/fonts/google-noto-cjk/NotoSansCJK-Regular.ttc",
        "/usr/share/fonts/noto-cjk/NotoSansCJK-Regular.ttc",
    ]
    for c in candidates:
        if os.path.exists(c):
            return c
    try:
        import subprocess
        return subprocess.check_output(["fc-match", "-f", "%{file}", "Noto Sans CJK KR"]).decode().strip()
    except Exception:
        return ""

def main():
    if len(sys.argv) < 2:
        print(json.dumps({"error": "Usage: render_lyrics.py <input_json_path_or_stdin>"}))
        sys.exit(1)

    input_arg = sys.argv[1]
    if input_arg == "-":
        data = json.load(sys.stdin)
    else:
        with open(input_arg, "r", encoding="utf-8") as f:
            data = json.load(f)

    cover_url = data.get("cover_url", "")
    duration_ms = data.get("duration_ms", 0)
    lines = data.get("lines", [])
    out_dir = data.get("out_dir", "/tmp/lyrics_frames")
    os.makedirs(out_dir, exist_ok=True)

    width = 1600
    height = 640

    # 1. Prepare Base Background
    base_bg = None
    if cover_url:
        try:
            temp_cover = os.path.join(out_dir, "cover_raw.jpg")
            if not os.path.exists(temp_cover):
                req = urllib.request.Request(cover_url, headers={"User-Agent": "Mozilla/5.0"})
                with urllib.request.urlopen(req, timeout=4) as resp, open(temp_cover, "wb") as out_f:
                    out_f.write(resp.read())

            cover_img = Image.open(temp_cover).convert("RGBA")
            # Resize cover to fill 1600x640 with aspect fill
            c_w, c_h = cover_img.size
            scale = max(width / c_w, height / c_h)
            new_w = int(c_w * scale)
            new_h = int(c_h * scale)
            cover_img = cover_img.resize((new_w, new_h), Image.Resampling.BILINEAR)

            # Crop center
            left = (new_w - width) // 2
            top = (new_h - height) // 2
            cover_cropped = cover_img.crop((left, top, left + width, top + height))

            # Apply heavy blur
            blurred = cover_cropped.filter(ImageFilter.GaussianBlur(22))

            # Overlay dark vignette/dim
            overlay = Image.new("RGBA", (width, height), (15, 15, 18, 175))
            base_bg = Image.alpha_composite(blurred, overlay)
        except Exception as e:
            base_bg = None

    if base_bg is None:
        # Sleek dark slate gradient fallback
        base_bg = Image.new("RGBA", (width, height), (24, 22, 28, 255))

    base_bg = base_bg.convert("RGB")
    bg_cache_path = os.path.join(out_dir, "bg_base.png")
    base_bg.save(bg_cache_path)

    font_path = get_font_path()
    font_main = ImageFont.truetype(font_path, 42, index=0)
    font_sub = ImageFont.truetype(font_path, 26, index=0)
    font_phonetic = ImageFont.truetype(font_path, 24, index=0)
    font_dim = ImageFont.truetype(font_path, 26, index=0)
    font_dim_small = ImageFont.truetype(font_path, 23, index=0)

    if duration_ms <= 0 and len(lines) > 0:
        duration_ms = lines[-1].get("time_ms", 0) + 5000

    def render_single_frame(idx):
        img = base_bg.copy()
        draw = ImageDraw.Draw(img)

        cur = lines[idx]
        cur_time = cur.get("time_ms", 0)

        # 1. Top progress bar
        prog_ratio = min(max(cur_time / max(duration_ms, 1), 0.0), 1.0)
        prog_w = int(prog_ratio * width)
        draw.rectangle([0, 0, prog_w, 7], fill=(235, 135, 158))
        draw.rectangle([prog_w, 0, width, 7], fill=(55, 55, 60))

        left_x = 75

        # 2. Previous Lines
        if idx >= 2:
            draw.text((left_x, 85), lines[idx-2].get("text", ""), font=font_dim_small, fill=(125, 125, 130))
        if idx >= 1:
            draw.text((left_x, 130), lines[idx-1].get("text", ""), font=font_dim, fill=(145, 145, 150))

        # 3. Active Line (Center focal point)
        cur_y = 210
        draw.text((left_x, cur_y), cur.get("text", ""), font=font_main, fill=(255, 255, 255))
        cur_y += 58

        trans = cur.get("trans", "").strip()
        if trans:
            draw.text((left_x, cur_y), trans, font=font_sub, fill=(185, 185, 192))
            cur_y += 42

        phonetic = cur.get("phonetic", "").strip()
        if phonetic:
            draw.text((left_x, cur_y), phonetic, font=font_phonetic, fill=(238, 162, 175))
            cur_y += 40

        # 4. Next Lines
        next_y = max(cur_y + 20, 420)
        if idx + 1 < len(lines):
            draw.text((left_x, next_y), lines[idx+1].get("text", ""), font=font_dim, fill=(145, 145, 150))
            next_y += 44
        if idx + 2 < len(lines):
            draw.text((left_x, next_y), lines[idx+2].get("text", ""), font=font_dim_small, fill=(125, 125, 130))

        frame_file = os.path.join(out_dir, f"frame_{idx:03d}.webp")
        img.save(frame_file, "WEBP", quality=82, method=0)
        try:
            cur_link = "/tmp/lyrics_current.webp"
            img.save(cur_link, "WEBP", quality=82, method=0)
        except Exception:
            pass
        return frame_file

    single_idx = data.get("single_idx", -1)
    t0 = time.perf_counter()
    if 0 <= single_idx < len(lines):
        frames = [render_single_frame(single_idx)]
    else:
        workers = min(4, os.cpu_count() or 4)
        with ThreadPoolExecutor(max_workers=workers) as pool:
            frames = list(pool.map(render_single_frame, range(len(lines))))
    t1 = time.perf_counter()

    print(json.dumps({
        "success": True,
        "count": len(frames),
        "out_dir": out_dir,
        "elapsed_ms": round((t1 - t0) * 1000, 2)
    }))

if __name__ == "__main__":
    main()
