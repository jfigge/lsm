// Renders DiceBear avatars to 256x256 JPEGs, fully offline.
// stdin: JSON array of {badge, gender, out}. stdout: one JSON line per file written.
// Called by generate.py; see README.md for the style choices and licences.
import { writeFile } from 'node:fs/promises';
import { createAvatar } from '@dicebear/core';
import { avataaars, avataaarsNeutral } from '@dicebear/collection';
import sharp from 'sharp';

const SIZE = 256;
const MIN_BYTES = 20 * 1024;
const MAX_BYTES = 30 * 1024;

// Profile-appropriate subsets of the avataaars options: natural skin and hair
// colours, calm expressions, no novelty eyes/mouths.
const common = {
  skinColor: ['614335', 'd08b5b', 'ae5d29', 'edb98a', 'ffdbb4'],
  hairColor: ['a55728', '2c1b18', 'b58143', 'd6b370', '724133', '4a312c', 'c93305', 'e8e1e1'],
  eyes: ['default', 'happy', 'squint', 'side'],
  eyebrows: ['default', 'defaultNatural', 'flatNatural', 'raisedExcited', 'raisedExcitedNatural'],
  mouth: ['default', 'smile', 'serious', 'twinkle'],
  clothing: ['blazerAndShirt', 'blazerAndSweater', 'collarAndSweater', 'hoodie', 'shirtCrewNeck', 'shirtScoopNeck', 'shirtVNeck'],
  accessories: ['prescription01', 'prescription02', 'round'],
  accessoriesProbability: 15,
  backgroundColor: ['b6e3f4', 'c0aede', 'd1d4f9', 'ffd5dc', 'ffdfbf', 'c7ebd1', 'e0e0e0'],
  backgroundType: ['solid'],
};

const STYLES = {
  male: {
    name: 'avataaars',
    style: avataaars,
    options: {
      ...common,
      top: ['shortCurly', 'shortFlat', 'shortRound', 'shortWaved', 'sides', 'theCaesar',
        'theCaesarAndSidePart', 'dreads01', 'dreads02', 'frizzle'],
      topProbability: 100,
      facialHair: ['beardLight', 'beardMedium', 'beardMajestic', 'moustacheFancy'],
      facialHairProbability: 35,
    },
  },
  female: {
    name: 'avataaars',
    style: avataaars,
    options: {
      ...common,
      top: ['bigHair', 'bob', 'bun', 'curly', 'curvy', 'frida', 'froBand', 'longButNotTooLong',
        'miaWallace', 'straight01', 'straight02', 'straightAndStrand', 'hijab'],
      topProbability: 100,
      facialHairProbability: 0,
    },
  },
  // Face only: no hair, facial hair or clothing, so no gender cues.
  unknown: {
    name: 'avataaars-neutral',
    style: avataaarsNeutral,
    options: {
      backgroundColor: common.skinColor,
      eyes: common.eyes,
      eyebrows: common.eyebrows,
      mouth: common.mouth,
    },
  },
};

// Highest quality that fits under MAX_BYTES; stop early once at or above MIN_BYTES.
async function encode(png) {
  let best = null;
  for (let q = 95; q >= 40; q -= 5) {
    const buf = await sharp(png).jpeg({ quality: q, mozjpeg: true, chromaSubsampling: '4:4:4' }).toBuffer();
    if (buf.length <= MAX_BYTES) {
      best = { buf, q };
      break;
    }
  }
  if (!best) throw new Error('could not fit under size cap');
  return best;
}

const jobs = JSON.parse(await new Response(process.stdin).text());
for (const { badge, gender, out } of jobs) {
  const s = STYLES[gender];
  if (!s) throw new Error(`badge ${badge}: unsupported gender ${JSON.stringify(gender)}`);
  const svg = createAvatar(s.style, { seed: String(badge), size: SIZE, ...s.options }).toString();
  const png = await sharp(Buffer.from(svg)).resize(SIZE, SIZE).flatten({ background: '#ffffff' }).png().toBuffer();
  const { buf, q } = await encode(png);
  await writeFile(out, buf);
  console.log(JSON.stringify({ badge, style: s.name, bytes: buf.length, quality: q, under_min: buf.length < MIN_BYTES }));
}
