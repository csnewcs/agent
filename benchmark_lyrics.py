#!/usr/bin/env python3
"""
Lyrics Card Rendering Benchmark
Measures performance for:
1. 1코어 초기 전체 렌더링 (블러 + 텍스트 전체)
2. 1코어 배경 캐시 렌더링 (텍스트만 갱신)
3. 1코어 RGB 고속 인코딩 온디맨드 (Method=0 최적화)
4. 16스레드 풀 40프레임 일괄 병렬 생성
"""

import os
import time
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

def run_benchmark():
    out_dir = "/tmp/lyrics_benchmark"
    os.makedirs(out_dir, exist_ok=True)

    width = 1600
    height = 640

    sample_lines = [
        {"time_ms": i * 4000, "text": f"Line {i+1}: 眠れない夜の静寂を切り裂いていく", "trans": f"{i+1}번째 줄: 잠들지 못하는 밤의 적막을 가르고 나아가", "phonetic": f"nemurenai yoru no shijima o kirisaite iku {i+1}"}
        for i in range(40)
    ]
    duration_ms = 160000

    font_path = get_font_path()
    font_main = ImageFont.truetype(font_path, 42, index=0)
    font_sub = ImageFont.truetype(font_path, 26, index=0)
    font_phonetic = ImageFont.truetype(font_path, 24, index=0)
    font_dim = ImageFont.truetype(font_path, 26, index=0)
    font_dim_small = ImageFont.truetype(font_path, 23, index=0)

    print("=" * 65)
    print("🚀 가사 카드 렌더링 최적화 벤치마크 테스트")
    print(f"• 해상도: {width}x{height} | 폰트: {font_path.split('/')[-1]}")
    print(f"• 테스트 프레임 수: {len(sample_lines)}장")
    print("=" * 65)

    # 1. 1회 배경 블러 생성
    t0 = time.perf_counter()
    raw_bg = Image.new("RGBA", (width, height), (45, 30, 60, 255))
    blurred = raw_bg.filter(ImageFilter.GaussianBlur(22))
    overlay = Image.new("RGBA", (width, height), (15, 15, 18, 175))
    base_bg = Image.alpha_composite(blurred, overlay)
    base_bg_rgb = base_bg.convert("RGB")
    t1 = time.perf_counter()
    bg_init_ms = (t1 - t0) * 1000

    # 2. [기존] 1코어 전체 렌더링 (매 프레임마다 블러 + 텍스트)
    t0 = time.perf_counter()
    raw = Image.new("RGBA", (width, height), (45, 30, 60, 255))
    b = raw.filter(ImageFilter.GaussianBlur(22))
    o = Image.new("RGBA", (width, height), (15, 15, 18, 175))
    f_img = Image.alpha_composite(b, o)
    d = ImageDraw.Draw(f_img)
    d.rectangle([0, 0, 800, 7], fill=(235, 135, 158, 255))
    d.rectangle([800, 0, width, 7], fill=(55, 55, 60, 160))
    d.text((75, 130), sample_lines[0]["text"], font=font_dim, fill=(145, 145, 150, 180))
    d.text((75, 210), sample_lines[1]["text"], font=font_main, fill=(255, 255, 255, 255))
    d.text((75, 268), sample_lines[1]["trans"], font=font_sub, fill=(185, 185, 192, 230))
    d.text((75, 310), sample_lines[1]["phonetic"], font=font_phonetic, fill=(238, 162, 175, 255))
    d.text((75, 420), sample_lines[2]["text"], font=font_dim, fill=(145, 145, 150, 180))
    f_img.save(os.path.join(out_dir, "legacy_full.webp"), "WEBP", quality=82)
    t1 = time.perf_counter()
    legacy_single_ms = (t1 - t0) * 1000

    # 3. [개선 1] 1코어 배경 캐시 렌더링 (RGBA)
    t0 = time.perf_counter()
    frame_img = base_bg.copy()
    draw = ImageDraw.Draw(frame_img)
    draw.rectangle([0, 0, 800, 7], fill=(235, 135, 158, 255))
    draw.text((75, 210), sample_lines[1]["text"], font=font_main, fill=(255, 255, 255, 255))
    draw.text((75, 268), sample_lines[1]["trans"], font=font_sub, fill=(185, 185, 192, 230))
    draw.text((75, 310), sample_lines[1]["phonetic"], font=font_phonetic, fill=(238, 162, 175, 255))
    frame_img.save(os.path.join(out_dir, "single_rgba.webp"), "WEBP", quality=82)
    t1 = time.perf_counter()
    cached_rgba_ms = (t1 - t0) * 1000

    # 4. [개선 2 - 초고속] 1코어 RGB 온디맨드 (method=0 고속 인코딩)
    t0 = time.perf_counter()
    frame_rgb = base_bg_rgb.copy()
    draw_rgb = ImageDraw.Draw(frame_rgb)
    draw_rgb.rectangle([0, 0, 800, 7], fill=(235, 135, 158))
    draw_rgb.rectangle([800, 0, width, 7], fill=(55, 55, 60))
    draw_rgb.text((75, 130), sample_lines[0]["text"], font=font_dim, fill=(145, 145, 150))
    draw_rgb.text((75, 210), sample_lines[1]["text"], font=font_main, fill=(255, 255, 255))
    draw_rgb.text((75, 268), sample_lines[1]["trans"], font=font_sub, fill=(185, 185, 192))
    draw_rgb.text((75, 310), sample_lines[1]["phonetic"], font=font_phonetic, fill=(238, 162, 175))
    draw_rgb.text((75, 420), sample_lines[2]["text"], font=font_dim, fill=(145, 145, 150))
    frame_rgb.save(os.path.join(out_dir, "single_rgb_fast.webp"), "WEBP", quality=82, method=0)
    t1 = time.perf_counter()
    fast_rgb_ms = (t1 - t0) * 1000

    # 5. [16스레드 풀] 40프레임 일괄 병렬 생성
    def render_worker(idx):
        img = base_bg_rgb.copy()
        d = ImageDraw.Draw(img)
        cur = sample_lines[idx]
        cur_time = cur["time_ms"]
        prog_ratio = min(max(cur_time / max(duration_ms, 1), 0.0), 1.0)
        prog_w = int(prog_ratio * width)
        d.rectangle([0, 0, prog_w, 7], fill=(235, 135, 158))
        d.rectangle([prog_w, 0, width, 7], fill=(55, 55, 60))
        left_x = 75
        if idx >= 1:
            d.text((left_x, 130), sample_lines[idx-1]["text"], font=font_dim, fill=(145, 145, 150))
        d.text((left_x, 210), cur["text"], font=font_main, fill=(255, 255, 255))
        d.text((left_x, 268), cur["trans"], font=font_sub, fill=(185, 185, 192))
        d.text((left_x, 310), cur["phonetic"], font=font_phonetic, fill=(238, 162, 175))
        if idx + 1 < len(sample_lines):
            d.text((left_x, 420), sample_lines[idx+1]["text"], font=font_dim, fill=(145, 145, 150))
        fpath = os.path.join(out_dir, f"batch_{idx:03d}.webp")
        img.save(fpath, "WEBP", quality=82, method=0)
        return fpath

    t0 = time.perf_counter()
    with ThreadPoolExecutor(max_workers=16) as pool:
        results = list(pool.map(render_worker, range(len(sample_lines))))
    t1 = time.perf_counter()
    batch_16_ms = (t1 - t0) * 1000

    print(f"• 배경 블러 1회 초기화 시간 : {bg_init_ms:.2f} ms (곡 시작 시 1회만 연산)")
    print("-" * 65)
    print(f"[1] 기존 방식 (블러 포함 전체 렌더링)    : {legacy_single_ms:.2f} ms (기준치)")
    print(f"[2] 개선 방식 (배경 캐시 RGBA)          : {cached_rgba_ms:.2f} ms ({legacy_single_ms/cached_rgba_ms:.1f}x 빠름)")
    print(f"[3] 초고속 온디맨드 (RGB + method=0)     : {fast_rgb_ms:.2f} ms ({legacy_single_ms/fast_rgb_ms:.1f}x 빠름! ⚡)")
    print("-" * 65)
    print(f"[4] 16스레드 풀 40프레임 일괄 병렬 생성  : {batch_16_ms:.2f} ms ({batch_16_ms/1000:.2f}초)")
    print(f"    └─ 프레임당 평균 처리 시간           : {batch_16_ms / len(sample_lines):.2f} ms")
    print("=" * 65)

if __name__ == "__main__":
    run_benchmark()
