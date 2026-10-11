(function(root) {
  'use strict';
  // Line numbers always refer to the immutable file, including folded context.
  function project(file, group, actor, passages, context = 3) {
    const blocks = file.blocks || [], lines = file.lines || [], ownership = new Array(lines.length);
    const matches = block => !actor || block[group === 'people' ? 'personId' : 'organizationId'] === actor;
    let attributed = 0;
    const visible = new Uint8Array(lines.length);
    blocks.forEach((block, index) => {
      for (let n = block.start - 1; n < block.end; n++) ownership[n] = index;
      if (matches(block)) {
        attributed += block.end - block.start + 1;
        visible.fill(1, Math.max(0, block.start - 1 - context), Math.min(lines.length, block.end + context));
      }
    });
    const rows = [];
    lines.forEach((text, index) => {
      if (passages && actor && !visible[index]) return;
      const blockIndex = ownership[index], block = blocks[blockIndex];
      rows.push({number:index + 1, text, blockIndex, matched:!!block && matches(block)});
    });
    return {rows, attributed, total:lines.length};
  }
  const api = {project};
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.AtlasSource = api;
})(typeof globalThis !== 'undefined' ? globalThis : this);
