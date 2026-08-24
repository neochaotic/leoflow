// Docsy runs the compiled SCSS through PostCSS (autoprefixer) on production builds
// (`hugo --gc --minify`). Hugo's `resources.PostCSS` discovers this config and the
// autoprefixer package in node_modules; without it the production CSS pipeline fails.
module.exports = {
  plugins: [
    require('autoprefixer'),
  ],
};
