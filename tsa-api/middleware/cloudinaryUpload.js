const cloudinary = require('../config/cloudinary');
const streamifier = require('streamifier'); // For streaming buffer to Cloudinary

const uploadToCloudinary = (fileBuffer, folder = 'uploads') => {
    return new Promise((resolve, reject) => {
        const stream = cloudinary.uploader.upload_stream(
            { folder: folder },
            (error, result) => {
                if (error) {
                    reject(error);
                } else {
                    resolve(result);
                }
            }
        );

        streamifier.createReadStream(fileBuffer).pipe(stream);
    });
};

module.exports = uploadToCloudinary;