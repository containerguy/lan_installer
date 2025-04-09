// eslint.config.js
export default [
  {
    files: ["electron-app/*.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module"
    },
    linterOptions: {
      reportUnusedDisableDirectives: true
    },
    rules: {
      "no-unused-vars": "warn",
      "semi": ["error", "always"],
      "quotes": ["error", "double"]
    }
  }
];
